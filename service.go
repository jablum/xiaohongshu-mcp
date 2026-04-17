package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/go-rod/rod"
	"github.com/sirupsen/logrus"
	"github.com/xpzouying/headless_browser"
	"github.com/xpzouying/xiaohongshu-mcp/browser"
	"github.com/xpzouying/xiaohongshu-mcp/configs"
	"github.com/xpzouying/xiaohongshu-mcp/cookies"
	"github.com/xpzouying/xiaohongshu-mcp/pkg/downloader"
	"github.com/xpzouying/xiaohongshu-mcp/pkg/xhsutil"
	"github.com/xpzouying/xiaohongshu-mcp/xiaohongshu"
)

// XiaohongshuService 小红书业务服务
type XiaohongshuService struct{}

// NewXiaohongshuService 创建小红书服务实例
func NewXiaohongshuService() *XiaohongshuService {
	return &XiaohongshuService{}
}

// PublishRequest 发布请求
type PublishRequest struct {
	Title      string   `json:"title" binding:"required"`
	Content    string   `json:"content" binding:"required"`
	Images     []string `json:"images" binding:"required,min=1"`
	Tags       []string `json:"tags,omitempty"`
	ScheduleAt string   `json:"schedule_at,omitempty"` // 定时发布时间，ISO8601格式，为空则立即发布
	IsOriginal bool     `json:"is_original,omitempty"` // 是否声明原创
	Visibility string   `json:"visibility,omitempty"`  // 可见范围: "公开可见"(默认), "仅自己可见", "仅互关好友可见"
	Products   []string `json:"products,omitempty"`    // 商品关键词列表，用于绑定带货商品
}

// LoginStatusResponse 登录状态响应
type LoginStatusResponse struct {
	IsLoggedIn bool   `json:"is_logged_in"`
	Username   string `json:"username,omitempty"`
}

// LoginQrcodeResponse 登录扫码二维码
type LoginQrcodeResponse struct {
	Timeout    string `json:"timeout"`
	IsLoggedIn bool   `json:"is_logged_in"`
	Img        string `json:"img,omitempty"`
}

// PublishResponse 发布响应
type PublishResponse struct {
	Title   string `json:"title"`
	Content string `json:"content"`
	Images  int    `json:"images"`
	Status  string `json:"status"`
	PostID  string `json:"post_id,omitempty"`
}

// PublishVideoRequest 发布视频请求（仅支持本地单个视频文件）
type PublishVideoRequest struct {
	Title      string   `json:"title" binding:"required"`
	Content    string   `json:"content" binding:"required"`
	Video      string   `json:"video" binding:"required"`
	Tags       []string `json:"tags,omitempty"`
	ScheduleAt string   `json:"schedule_at,omitempty"` // 定时发布时间，ISO8601格式，为空则立即发布
	Visibility string   `json:"visibility,omitempty"`  // 可见范围: "公开可见"(默认), "仅自己可见", "仅互关好友可见"
	Products   []string `json:"products,omitempty"`    // 商品关键词列表，用于绑定带货商品
}

// PublishVideoResponse 发布视频响应
type PublishVideoResponse struct {
	Title   string `json:"title"`
	Content string `json:"content"`
	Video   string `json:"video"`
	Status  string `json:"status"`
	PostID  string `json:"post_id,omitempty"`
}

// FeedsListResponse Feeds列表响应
type FeedsListResponse struct {
	Feeds []xiaohongshu.Feed `json:"feeds"`
	Count int                `json:"count"`
}

// UserProfileResponse 用户主页响应
type UserProfileResponse struct {
	UserBasicInfo xiaohongshu.UserBasicInfo      `json:"userBasicInfo"`
	Interactions  []xiaohongshu.UserInteractions `json:"interactions"`
	Feeds         []xiaohongshu.Feed             `json:"feeds"`
}

// DeleteCookies 删除 cookies 文件，用于登录重置
func (s *XiaohongshuService) DeleteCookies(ctx context.Context) error {
	cookiePath := cookies.GetCookiesFilePath()
	cookieLoader := cookies.NewLoadCookie(cookiePath)
	return cookieLoader.DeleteCookies()
}

// CheckLoginStatus 检查登录状态
func (s *XiaohongshuService) CheckLoginStatus(ctx context.Context) (*LoginStatusResponse, error) {
	b := newBrowser()
	defer b.Close()

	page := b.NewPage()
	defer page.Close()

	loginAction := xiaohongshu.NewLogin(page)

	isLoggedIn, err := loginAction.CheckLoginStatus(ctx)
	if err != nil {
		return nil, err
	}

	response := &LoginStatusResponse{
		IsLoggedIn: isLoggedIn,
		Username:   configs.Username,
	}

	return response, nil
}

// GetLoginQrcode 获取登录的扫码二维码
func (s *XiaohongshuService) GetLoginQrcode(ctx context.Context) (*LoginQrcodeResponse, error) {
	b := newBrowser()
	page := b.NewPage()

	deferFunc := func() {
		_ = page.Close()
		b.Close()
	}

	loginAction := xiaohongshu.NewLogin(page)

	img, loggedIn, err := loginAction.FetchQrcodeImage(ctx)
	if err != nil || loggedIn {
		defer deferFunc()
	}
	if err != nil {
		return nil, err
	}

	timeout := 4 * time.Minute

	if !loggedIn {
		go func() {
			ctxTimeout, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()
			defer deferFunc()

			if loginAction.WaitForLogin(ctxTimeout) {
				if er := saveCookies(page); er != nil {
					logrus.Errorf("failed to save cookies: %v", er)
				}
			}
		}()
	}

	return &LoginQrcodeResponse{
		Timeout: func() string {
			if loggedIn {
				return "0s"
			}
			return timeout.String()
		}(),
		Img:        img,
		IsLoggedIn: loggedIn,
	}, nil
}

// PublishContent 发布内容
func (s *XiaohongshuService) PublishContent(ctx context.Context, req *PublishRequest) (*PublishResponse, error) {
	// 验证标题长度（小红书限制：最大20个字）
	if xhsutil.CalcTitleLength(req.Title) > 20 {
		return nil, fmt.Errorf("标题长度超过限制")
	}

	// 处理图片：下载URL图片或使用本地路径
	imagePaths, err := s.processImages(req.Images)
	if err != nil {
		return nil, err
	}

	// 解析定时发布时间
	var scheduleTime *time.Time
	if req.ScheduleAt != "" {
		t, err := time.Parse(time.RFC3339, req.ScheduleAt)
		if err != nil {
			return nil, fmt.Errorf("定时发布时间格式错误，请使用 ISO8601 格式: %v", err)
		}

		// 校验定时发布时间范围：1小时至14天
		now := time.Now()
		minTime := now.Add(1 * time.Hour)
		maxTime := now.Add(14 * 24 * time.Hour)

		if t.Before(minTime) {
			return nil, fmt.Errorf("定时发布时间必须至少在1小时后，当前设置: %s，最早可选: %s",
				t.Format("2006-01-02 15:04"), minTime.Format("2006-01-02 15:04"))
		}
		if t.After(maxTime) {
			return nil, fmt.Errorf("定时发布时间不能超过14天，当前设置: %s，最晚可选: %s",
				t.Format("2006-01-02 15:04"), maxTime.Format("2006-01-02 15:04"))
		}

		scheduleTime = &t
		logrus.Infof("设置定时发布时间: %s", t.Format("2006-01-02 15:04"))
	}

	// 构建发布内容
	content := xiaohongshu.PublishImageContent{
		Title:        req.Title,
		Content:      req.Content,
		Tags:         req.Tags,
		ImagePaths:   imagePaths,
		ScheduleTime: scheduleTime,
		IsOriginal:   req.IsOriginal,
		Visibility:   req.Visibility,
		Products:     req.Products,
	}

	// 执行发布
	if err := s.publishContent(ctx, content); err != nil {
		logrus.Errorf("发布内容失败: title=%s %v", content.Title, err)
		return nil, err
	}

	response := &PublishResponse{
		Title:   req.Title,
		Content: req.Content,
		Images:  len(imagePaths),
		Status:  "发布完成",
	}

	return response, nil
}

// processImages 处理图片列表，支持URL下载和本地路径
func (s *XiaohongshuService) processImages(images []string) ([]string, error) {
	processor := downloader.NewImageProcessor()
	return processor.ProcessImages(images)
}

// publishContent 执行内容发布
func (s *XiaohongshuService) publishContent(ctx context.Context, content xiaohongshu.PublishImageContent) error {
	b := newBrowser()
	defer b.Close()

	page := b.NewPage()
	defer page.Close()

	action, err := xiaohongshu.NewPublishImageAction(page)
	if err != nil {
		return err
	}

	// 执行发布
	return action.Publish(ctx, content)
}

// PublishVideo 发布视频（本地文件）
func (s *XiaohongshuService) PublishVideo(ctx context.Context, req *PublishVideoRequest) (*PublishVideoResponse, error) {
	// 标题长度校验（小红书限制：最大20个字）
	if xhsutil.CalcTitleLength(req.Title) > 20 {
		return nil, fmt.Errorf("标题长度超过限制")
	}

	// 本地视频文件校验
	if req.Video == "" {
		return nil, fmt.Errorf("必须提供本地视频文件")
	}
	if _, err := os.Stat(req.Video); err != nil {
		return nil, fmt.Errorf("视频文件不存在或不可访问: %v", err)
	}

	// 解析定时发布时间
	var scheduleTime *time.Time
	if req.ScheduleAt != "" {
		t, err := time.Parse(time.RFC3339, req.ScheduleAt)
		if err != nil {
			return nil, fmt.Errorf("定时发布时间格式错误，请使用 ISO8601 格式: %v", err)
		}

		// 校验定时发布时间范围：1小时至14天
		now := time.Now()
		minTime := now.Add(1 * time.Hour)
		maxTime := now.Add(14 * 24 * time.Hour)

		if t.Before(minTime) {
			return nil, fmt.Errorf("定时发布时间必须至少在1小时后，当前设置: %s，最早可选: %s",
				t.Format("2006-01-02 15:04"), minTime.Format("2006-01-02 15:04"))
		}
		if t.After(maxTime) {
			return nil, fmt.Errorf("定时发布时间不能超过14天，当前设置: %s，最晚可选: %s",
				t.Format("2006-01-02 15:04"), maxTime.Format("2006-01-02 15:04"))
		}

		scheduleTime = &t
		logrus.Infof("设置定时发布时间: %s", t.Format("2006-01-02 15:04"))
	}

	// 构建发布内容
	content := xiaohongshu.PublishVideoContent{
		Title:        req.Title,
		Content:      req.Content,
		Tags:         req.Tags,
		VideoPath:    req.Video,
		ScheduleTime: scheduleTime,
		Visibility:   req.Visibility,
		Products:     req.Products,
	}

	// 执行发布
	if err := s.publishVideo(ctx, content); err != nil {
		return nil, err
	}

	resp := &PublishVideoResponse{
		Title:   req.Title,
		Content: req.Content,
		Video:   req.Video,
		Status:  "发布完成",
	}
	return resp, nil
}

// publishVideo 执行视频发布
func (s *XiaohongshuService) publishVideo(ctx context.Context, content xiaohongshu.PublishVideoContent) error {
	b := newBrowser()
	defer b.Close()

	page := b.NewPage()
	defer page.Close()

	action, err := xiaohongshu.NewPublishVideoAction(page)
	if err != nil {
		return err
	}

	return action.PublishVideo(ctx, content)
}

// ListFeeds 获取Feeds列表
func (s *XiaohongshuService) ListFeeds(ctx context.Context) (*FeedsListResponse, error) {
	b := newBrowser()
	defer b.Close()

	page := b.NewPage()
	defer page.Close()

	// 创建 Feeds 列表 action
	action := xiaohongshu.NewFeedsListAction(page)

	// 获取 Feeds 列表
	feeds, err := action.GetFeedsList(ctx)
	if err != nil {
		logrus.Errorf("获取 Feeds 列表失败: %v", err)
		return nil, err
	}

	response := &FeedsListResponse{
		Feeds: feeds,
		Count: len(feeds),
	}

	return response, nil
}

func (s *XiaohongshuService) SearchFeeds(ctx context.Context, count int, keyword string, filters ...xiaohongshu.FilterOption) (*FeedsListResponse, error) {
	if count <= 0 {
		count = 5
	}
	if count > 20 {
		count = 20
	}
	b := newBrowser()
	defer b.Close()

	page := b.NewPage()
	defer page.Close()

	action := xiaohongshu.NewSearchAction(page)

	feeds, err := action.Search(ctx, keyword, filters...)
	if err != nil {
		return nil, err
	}

	response := &FeedsListResponse{
		Feeds: feeds,
		Count: len(feeds),
	}
	// 小红书网页未提供条数控制，这里手动控制条数
	if len(response.Feeds) > count {
		response.Feeds = response.Feeds[0:count]
		response.Count = count
	}
	return response, nil
}

// GetFeedDetail 获取Feed详情
func (s *XiaohongshuService) GetFeedDetail(ctx context.Context, feedID, xsecToken string, loadAllComments bool) (*FeedDetailResponse, error) {
	return s.GetFeedDetailWithConfig(ctx, feedID, xsecToken, loadAllComments, xiaohongshu.DefaultCommentLoadConfig())
}

// GetFeedDetailWithConfig 使用配置获取Feed详情
func (s *XiaohongshuService) GetFeedDetailWithConfig(ctx context.Context, feedID, xsecToken string, loadAllComments bool, config xiaohongshu.CommentLoadConfig) (*FeedDetailResponse, error) {
	b := newBrowser()
	defer b.Close()

	page := b.NewPage()
	defer page.Close()

	// 创建 Feed 详情 action
	action := xiaohongshu.NewFeedDetailAction(page)

	// 获取 Feed 详情
	result, err := action.GetFeedDetailWithConfig(ctx, feedID, xsecToken, loadAllComments, config)
	if err != nil {
		return nil, err
	}

	response := &FeedDetailResponse{
		FeedID: feedID,
		Data:   result,
	}

	return response, nil
}

// UserProfile 获取用户信息
func (s *XiaohongshuService) UserProfile(ctx context.Context, userID, xsecToken string) (*UserProfileResponse, error) {
	b := newBrowser()
	defer b.Close()

	page := b.NewPage()
	defer page.Close()

	action := xiaohongshu.NewUserProfileAction(page)

	result, err := action.UserProfile(ctx, userID, xsecToken)
	if err != nil {
		return nil, err
	}
	response := &UserProfileResponse{
		UserBasicInfo: result.UserBasicInfo,
		Interactions:  result.Interactions,
		Feeds:         result.Feeds,
	}

	return response, nil

}

// PostCommentToFeed 发表评论到Feed
func (s *XiaohongshuService) PostCommentToFeed(ctx context.Context, feedID, xsecToken, content string) (*PostCommentResponse, error) {
	b := newBrowser()
	defer b.Close()

	page := b.NewPage()
	defer page.Close()

	action := xiaohongshu.NewCommentFeedAction(page)

	if err := action.PostComment(ctx, feedID, xsecToken, content); err != nil {
		return nil, err
	}

	return &PostCommentResponse{FeedID: feedID, Success: true, Message: "评论发表成功"}, nil
}

// LikeFeed 点赞笔记
func (s *XiaohongshuService) LikeFeed(ctx context.Context, feedID, xsecToken string) (*ActionResult, error) {
	b := newBrowser()
	defer b.Close()

	page := b.NewPage()
	defer page.Close()

	action := xiaohongshu.NewLikeAction(page)
	if err := action.Like(ctx, feedID, xsecToken); err != nil {
		return nil, err
	}
	return &ActionResult{FeedID: feedID, Success: true, Message: "点赞成功或已点赞"}, nil
}

// UnlikeFeed 取消点赞笔记
func (s *XiaohongshuService) UnlikeFeed(ctx context.Context, feedID, xsecToken string) (*ActionResult, error) {
	b := newBrowser()
	defer b.Close()

	page := b.NewPage()
	defer page.Close()

	action := xiaohongshu.NewLikeAction(page)
	if err := action.Unlike(ctx, feedID, xsecToken); err != nil {
		return nil, err
	}
	return &ActionResult{FeedID: feedID, Success: true, Message: "取消点赞成功或未点赞"}, nil
}

// FavoriteFeed 收藏笔记
func (s *XiaohongshuService) FavoriteFeed(ctx context.Context, feedID, xsecToken string) (*ActionResult, error) {
	b := newBrowser()
	defer b.Close()

	page := b.NewPage()
	defer page.Close()

	action := xiaohongshu.NewFavoriteAction(page)
	if err := action.Favorite(ctx, feedID, xsecToken); err != nil {
		return nil, err
	}
	return &ActionResult{FeedID: feedID, Success: true, Message: "收藏成功或已收藏"}, nil
}

// UnfavoriteFeed 取消收藏笔记
func (s *XiaohongshuService) UnfavoriteFeed(ctx context.Context, feedID, xsecToken string) (*ActionResult, error) {
	b := newBrowser()
	defer b.Close()

	page := b.NewPage()
	defer page.Close()

	action := xiaohongshu.NewFavoriteAction(page)
	if err := action.Unfavorite(ctx, feedID, xsecToken); err != nil {
		return nil, err
	}
	return &ActionResult{FeedID: feedID, Success: true, Message: "取消收藏成功或未收藏"}, nil
}

// ListCollectedNotes 获取当前登录用户的收藏列表
func (s *XiaohongshuService) ListCollectedNotes(ctx context.Context) (*CollectedNotesResponse, error) {
	//b := newBrowser()
	//defer b.Close()
	//
	//page := b.NewPage()
	//defer page.Close()
	//
	//action := xiaohongshu.NewCollectAction(page)
	//
	//result, err := action.GetCollectedNotes(ctx)
	//if err != nil {
	//	return nil, err
	//}
	mockData := `
{
  "notes": [
    {
      "note_id": "67caff7f00000000060283c5",
      "display_title": "京味儿Citywalk｜一日游经典路线‼️",
      "type": "normal",
      "xsec_token": "ABE8_z7b22vL4whZ1uB48DMCaVGtXlvTqvngt4h9HniBU=",
      "cover": {
        "width": 1280,
        "height": 1706,
        "url_pre": "http://sns-webpic-qc.xhscdn.com/202604171404/be68e675ced7e14a37b36148e0fa92dc/spectrum/1040g34o31entjhup5o3048t3r47t6815470o5to!nc_n_webp_prv_1",
        "url_default": "http://sns-webpic-qc.xhscdn.com/202604171404/da94a152c30794c53d1ec6c5598a6e3f/spectrum/1040g34o31entjhup5o3048t3r47t6815470o5to!nc_n_webp_mw_1",
        "info_list": [
          {
            "imageScene": "",
            "url": "http://sns-webpic-qc.xhscdn.com/202604171404/be68e675ced7e14a37b36148e0fa92dc/spectrum/1040g34o31entjhup5o3048t3r47t6815470o5to!nc_n_webp_prv_1"
          },
          {
            "imageScene": "",
            "url": "http://sns-webpic-qc.xhscdn.com/202604171404/da94a152c30794c53d1ec6c5598a6e3f/spectrum/1040g34o31entjhup5o3048t3r47t6815470o5to!nc_n_webp_mw_1"
          }
        ]
      },
      "user": {
        "user_id": "58384fd35e87e77148f82025",
        "nickname": "💖旅游小堆唲💖",
        "avatar": "https://sns-avatar-qc.xhscdn.com/avatar/66b5a94aabe1e6168f27b52f.jpg?imageView2/2/w/120/format/jpg",
        "xsec_token": "ABFobFcO1pdYyGmd3DTKIymi2LE3Q1yENdy8jSLiKJDPw="
      },
      "interact_info": {
        "liked": false,
        "liked_count": "246"
      }
    },
    {
      "note_id": "67cfa22200000000280355a6",
      "display_title": "北京24小时转机✈️一日游攻略｜特种兵来咯",
      "type": "normal",
      "xsec_token": "ABgKWQ5n59s6hDV7Dwm8eFKL_tnsQ6kEjVWg0iuZWIHus=",
      "cover": {
        "width": 1440,
        "height": 1920,
        "url_pre": "http://sns-webpic-qc.xhscdn.com/202604171404/93114ebcb6c83434e0ea64a5d28c36bb/notes_pre_post/1040g3k031esejp0cm61g5o8rhh8gbqpf1a74lt8!nc_n_webp_prv_1",
        "url_default": "http://sns-webpic-qc.xhscdn.com/202604171404/ade05ca5998b1325dd465de172470cec/notes_pre_post/1040g3k031esejp0cm61g5o8rhh8gbqpf1a74lt8!nc_n_webp_mw_1",
        "info_list": [
          {
            "imageScene": "",
            "url": "http://sns-webpic-qc.xhscdn.com/202604171404/93114ebcb6c83434e0ea64a5d28c36bb/notes_pre_post/1040g3k031esejp0cm61g5o8rhh8gbqpf1a74lt8!nc_n_webp_prv_1"
          },
          {
            "imageScene": "",
            "url": "http://sns-webpic-qc.xhscdn.com/202604171404/ade05ca5998b1325dd465de172470cec/notes_pre_post/1040g3k031esejp0cm61g5o8rhh8gbqpf1a74lt8!nc_n_webp_mw_1"
          }
        ]
      },
      "user": {
        "user_id": "611b8c51000000000101eb2f",
        "nickname": "比巴小卜卜",
        "avatar": "https://sns-avatar-qc.xhscdn.com/avatar/61507c078f474cf115b253b6.jpg?imageView2/2/w/120/format/jpg",
        "xsec_token": "ABMpZaRKwN_H5AxqnZM7OzZYUFtsTCrjXFibPt1vhKgPY="
      },
      "interact_info": {
        "liked": false,
        "liked_count": "52"
      }
    },
    {
      "note_id": "68c0deab000000001d02c554",
      "display_title": "不早起版｜一日游青城山前山+都江堰蓝眼泪",
      "type": "normal",
      "xsec_token": "ABmHK-DIf2trebqtrZZ2IV_PyNjvGsF3tdBO8OvVH_9gE=",
      "cover": {
        "width": 3442,
        "height": 4590,
        "url_pre": "http://sns-webpic-qc.xhscdn.com/202604171404/dda65ceb36076b8646807a2501c43f1c/1040g00831m810io5l8d05nuo3vt09fqdttiucdo!nc_n_webp_prv_1",
        "url_default": "http://sns-webpic-qc.xhscdn.com/202604171404/ecc05511482ddbefddb118452bbf1f50/1040g00831m810io5l8d05nuo3vt09fqdttiucdo!nc_n_webp_mw_1",
        "info_list": [
          {
            "imageScene": "",
            "url": "http://sns-webpic-qc.xhscdn.com/202604171404/dda65ceb36076b8646807a2501c43f1c/1040g00831m810io5l8d05nuo3vt09fqdttiucdo!nc_n_webp_prv_1"
          },
          {
            "imageScene": "",
            "url": "http://sns-webpic-qc.xhscdn.com/202604171404/ecc05511482ddbefddb118452bbf1f50/1040g00831m810io5l8d05nuo3vt09fqdttiucdo!nc_n_webp_mw_1"
          }
        ]
      },
      "user": {
        "user_id": "5fd81ffa000000000100bf4d",
        "nickname": "小舟行记（流浪版🌍）",
        "avatar": "https://sns-avatar-qc.xhscdn.com/avatar/1040g2jo31frf6db1gg6g5nuo3vt09fqdb85jc50?imageView2/2/w/120/format/jpg",
        "xsec_token": "ABjnIsAWcHDPZUEQ34z94DuSj8w5gkj-rPn1FStAm7n7g="
      },
      "interact_info": {
        "liked": false,
        "liked_count": "1412"
      }
    },
    {
      "note_id": "6959d946000000001f004112",
      "display_title": "成都一日游，保姆级攻略！感受慢生活～",
      "type": "normal",
      "xsec_token": "AB5fROYVEzTE6EIeJs63-OQaARdAN0eSPymeo35Tbkgpk=",
      "cover": {
        "width": 1280,
        "height": 1707,
        "url_pre": "http://sns-webpic-qc.xhscdn.com/202604171404/8205015122b16c2d93520875b94c267a/notes_pre_post/1040g3k031qtdua8bg0005ol99uimspoc166d1o8!nc_n_webp_prv_1",
        "url_default": "http://sns-webpic-qc.xhscdn.com/202604171404/a2f3eb7d03982cb8b819c38dbf828b59/notes_pre_post/1040g3k031qtdua8bg0005ol99uimspoc166d1o8!nc_n_webp_mw_1",
        "info_list": [
          {
            "imageScene": "",
            "url": "http://sns-webpic-qc.xhscdn.com/202604171404/8205015122b16c2d93520875b94c267a/notes_pre_post/1040g3k031qtdua8bg0005ol99uimspoc166d1o8!nc_n_webp_prv_1"
          },
          {
            "imageScene": "",
            "url": "http://sns-webpic-qc.xhscdn.com/202604171404/a2f3eb7d03982cb8b819c38dbf828b59/notes_pre_post/1040g3k031qtdua8bg0005ol99uimspoc166d1o8!nc_n_webp_mw_1"
          }
        ]
      },
      "user": {
        "user_id": "62a94fa5000000001b02670c",
        "nickname": "铁板豆腐",
        "avatar": "https://sns-avatar-qc.xhscdn.com/avatar/1040g2jo317ejjkoc44005ol99uimspoc8v3m10g?imageView2/2/w/120/format/jpg",
        "xsec_token": "ABSoxi9E1yIny8rvJvJ_O9j-UitF5xa3rJfrJOtGJnVuU="
      },
      "interact_info": {
        "liked": false,
        "liked_count": "373"
      }
    },
    {
      "note_id": "69dcbeb500000000220000e7",
      "display_title": "北京五一，经典必逛Citywalk路线，就这❺条❗️",
      "type": "normal",
      "xsec_token": "ABzT8M66aOIv-zx2QHmlSe6dw2BnI6XITHcK_F1UT-7fU=",
      "cover": {
        "width": 1920,
        "height": 2560,
        "url_pre": "http://sns-webpic-qc.xhscdn.com/202604171404/b28651d3915a45d9786349d67ebdbf5e/notes_pre_post/1040g3k031utcj14r2q005oaikapgkc6p8qmfrv8!nc_n_webp_prv_1",
        "url_default": "http://sns-webpic-qc.xhscdn.com/202604171404/5a316ef7c09d952919741e5662af1d22/notes_pre_post/1040g3k031utcj14r2q005oaikapgkc6p8qmfrv8!nc_n_webp_mw_1",
        "info_list": [
          {
            "imageScene": "",
            "url": "http://sns-webpic-qc.xhscdn.com/202604171404/b28651d3915a45d9786349d67ebdbf5e/notes_pre_post/1040g3k031utcj14r2q005oaikapgkc6p8qmfrv8!nc_n_webp_prv_1"
          },
          {
            "imageScene": "",
            "url": "http://sns-webpic-qc.xhscdn.com/202604171404/5a316ef7c09d952919741e5662af1d22/notes_pre_post/1040g3k031utcj14r2q005oaikapgkc6p8qmfrv8!nc_n_webp_mw_1"
          }
        ]
      },
      "user": {
        "user_id": "6152a2b300000000020230d9",
        "nickname": "阿七在北京",
        "avatar": "https://sns-avatar-qc.xhscdn.com/avatar/1040g2jo311981gromc005oaikapgkc6pba7lre0?imageView2/2/w/120/format/jpg",
        "xsec_token": "ABFr2KFf4fvNDNausmFlRbOxRafB5PdMqtfVtD_x7Fb44="
      },
      "interact_info": {
        "liked": false,
        "liked_count": "148"
      }
    },
    {
      "note_id": "69c7a30e000000001f000511",
      "display_title": "北京四天人均2K｜保姆级逛吃玩攻略＋附路线",
      "type": "normal",
      "xsec_token": "ABsKEQkWdG7U7N635mpKVUg8HDEQnwVWJHcBDtOjtQSWE=",
      "cover": {
        "width": 1920,
        "height": 2560,
        "url_pre": "http://sns-webpic-qc.xhscdn.com/202604171404/736cadf6458880ff7d3fc435327d49e8/1040g2sg31u8is1toia3g45bbl27pds4t63ns08o!nc_n_webp_prv_1",
        "url_default": "http://sns-webpic-qc.xhscdn.com/202604171404/e6bc8e999a71976482d74a514f52f707/1040g2sg31u8is1toia3g45bbl27pds4t63ns08o!nc_n_webp_mw_1",
        "info_list": [
          {
            "imageScene": "",
            "url": "http://sns-webpic-qc.xhscdn.com/202604171404/736cadf6458880ff7d3fc435327d49e8/1040g2sg31u8is1toia3g45bbl27pds4t63ns08o!nc_n_webp_prv_1"
          },
          {
            "imageScene": "",
            "url": "http://sns-webpic-qc.xhscdn.com/202604171404/e6bc8e999a71976482d74a514f52f707/1040g2sg31u8is1toia3g45bbl27pds4t63ns08o!nc_n_webp_mw_1"
          }
        ]
      },
      "user": {
        "user_id": "56a48f965e87e726d289f09d",
        "nickname": "大欢干啥呢",
        "avatar": "https://sns-avatar-qc.xhscdn.com/avatar/1040g2jo31p3cli3njc6045bbl27pds4trq8gdl0?imageView2/2/w/120/format/jpg",
        "xsec_token": "ABOz2X0is4tcNHXuN84Se8bF-nx5NF63HQUxdzs0X2jj0="
      },
      "interact_info": {
        "liked": false,
        "liked_count": "538"
      }
    },
    {
      "note_id": "69c0e2d4000000002102c0d2",
      "display_title": "北京🍂｜5天4晚详细旅行攻略（图文版）",
      "type": "normal",
      "xsec_token": "ABWAjhoYZTjBObFGjisc8or6EPoftzKj2b_JsbACXdaGI=",
      "cover": {
        "width": 3072,
        "height": 4096,
        "url_pre": "http://sns-webpic-qc.xhscdn.com/202604171404/0c34d598137c91cf7b9c21bdc7c04a7e/notes_pre_post/1040g3k831u23gt9p7e704bo8cvraam60pdi7u68!nc_n_webp_prv_1",
        "url_default": "http://sns-webpic-qc.xhscdn.com/202604171404/f0de0ccf62c21bdd03af561bca8564ac/notes_pre_post/1040g3k831u23gt9p7e704bo8cvraam60pdi7u68!nc_n_webp_mw_1",
        "info_list": [
          {
            "imageScene": "",
            "url": "http://sns-webpic-qc.xhscdn.com/202604171404/0c34d598137c91cf7b9c21bdc7c04a7e/notes_pre_post/1040g3k831u23gt9p7e704bo8cvraam60pdi7u68!nc_n_webp_prv_1"
          },
          {
            "imageScene": "",
            "url": "http://sns-webpic-qc.xhscdn.com/202604171404/f0de0ccf62c21bdd03af561bca8564ac/notes_pre_post/1040g3k831u23gt9p7e704bo8cvraam60pdi7u68!nc_n_webp_mw_1"
          }
        ]
      },
      "user": {
        "user_id": "5be076a5d784f10001c758c0",
        "nickname": "辣鸡脆堡yeah",
        "avatar": "https://sns-avatar-qc.xhscdn.com/avatar/67616f0b59d26cd13a27fa6b.jpg?imageView2/2/w/120/format/jpg",
        "xsec_token": "ABdIJUmuhyYqqfUNGN27ZGJVB1T8zOycLU1GChDxaYqsk="
      },
      "interact_info": {
        "liked": false,
        "liked_count": "1359"
      }
    },
    {
      "note_id": "6800ea53000000001e005f71",
      "display_title": "拒绝人挤人！📍北京五一❾个小众浪漫好去处🍃",
      "type": "normal",
      "xsec_token": "ABPPx5lfoFqJe9UxmuyL_x0KlAlJvBXx25fBEkcDAzHbg=",
      "cover": {
        "width": 1920,
        "height": 2560,
        "url_pre": "http://sns-webpic-qc.xhscdn.com/202604171404/353eb503a417701ced428f76343299cb/1040g00831gcipekrji0049mi5onng189a13h28g!nc_n_webp_prv_1",
        "url_default": "http://sns-webpic-qc.xhscdn.com/202604171404/eb70e8058ab457e3ca8484e40039b4d7/1040g00831gcipekrji0049mi5onng189a13h28g!nc_n_webp_mw_1",
        "info_list": [
          {
            "imageScene": "",
            "url": "http://sns-webpic-qc.xhscdn.com/202604171404/353eb503a417701ced428f76343299cb/1040g00831gcipekrji0049mi5onng189a13h28g!nc_n_webp_prv_1"
          },
          {
            "imageScene": "",
            "url": "http://sns-webpic-qc.xhscdn.com/202604171404/eb70e8058ab457e3ca8484e40039b4d7/1040g00831gcipekrji0049mi5onng189a13h28g!nc_n_webp_mw_1"
          }
        ]
      },
      "user": {
        "user_id": "5972af786a6a6901e8ef0509",
        "nickname": "想吃披萨汉堡和好多肉",
        "avatar": "https://sns-avatar-qc.xhscdn.com/avatar/1040g2jo30og0o2v1240049mi5onng189npnvph0?imageView2/2/w/120/format/jpg",
        "xsec_token": "ABMmcWWvC5j_ODHCPhaFfMMbEAP-tNxq7cu5nd9XZujTw="
      },
      "interact_info": {
        "liked": false,
        "liked_count": "1255"
      }
    },
    {
      "note_id": "69ddf739000000001a031d31",
      "display_title": "北京近郊| 这和“赛里木湖”没差吧？",
      "type": "normal",
      "xsec_token": "ABmN0jY5uDjU-xqpD_qVvc7ydO38Ksryf6nR9udkBTAtM=",
      "cover": {
        "width": 1536,
        "height": 2048,
        "url_pre": "http://sns-webpic-qc.xhscdn.com/202604171404/bca7005938e9c7ffe72f645ca55d5be7/1040g2sg31uueksguiqe049ul1fn8p3eia2etbv8!nc_n_webp_prv_1",
        "url_default": "http://sns-webpic-qc.xhscdn.com/202604171404/f3d13fba4c322a7af6cd3e8f6fbd79f7/1040g2sg31uueksguiqe049ul1fn8p3eia2etbv8!nc_n_webp_mw_1",
        "info_list": [
          {
            "imageScene": "",
            "url": "http://sns-webpic-qc.xhscdn.com/202604171404/bca7005938e9c7ffe72f645ca55d5be7/1040g2sg31uueksguiqe049ul1fn8p3eia2etbv8!nc_n_webp_prv_1"
          },
          {
            "imageScene": "",
            "url": "http://sns-webpic-qc.xhscdn.com/202604171404/f3d13fba4c322a7af6cd3e8f6fbd79f7/1040g2sg31uueksguiqe049ul1fn8p3eia2etbv8!nc_n_webp_mw_1"
          }
        ]
      },
      "user": {
        "user_id": "59e72e8c4eacab247a148dd2",
        "nickname": "就得做大哥",
        "avatar": "https://sns-avatar-qc.xhscdn.com/avatar/1040g2jo31rsv7ssg320g49ul1fn8p3eiedqb8g0?imageView2/2/w/120/format/jpg",
        "xsec_token": "ABc24JSDJkq6rF4jzjtCngLJ0pCCZyHM1kzSpn-Pfgq7s="
      },
      "interact_info": {
        "liked": false,
        "liked_count": "1857"
      }
    }
  ],
  "has_more": false,
  "cursor": "69ddf739000000001a031d31",
  "count": 9
}
`
	var response CollectedNotesResponse
	if err := json.Unmarshal([]byte(mockData), &response); err != nil {
		logrus.Warnf("unmarshal mock collect data error: %v", err)
		return nil, err
	}
	return &response, nil
}

// ReplyCommentToFeed 回复指定评论
func (s *XiaohongshuService) ReplyCommentToFeed(ctx context.Context, feedID, xsecToken, commentID, userID, content string) (*ReplyCommentResponse, error) {
	b := newBrowser()
	defer b.Close()

	page := b.NewPage()
	defer page.Close()

	action := xiaohongshu.NewCommentFeedAction(page)

	if err := action.ReplyToComment(ctx, feedID, xsecToken, commentID, userID, content); err != nil {
		return nil, err
	}

	return &ReplyCommentResponse{
		FeedID:          feedID,
		TargetCommentID: commentID,
		TargetUserID:    userID,
		Success:         true,
		Message:         "评论回复成功",
	}, nil
}

func newBrowser() *headless_browser.Browser {
	return browser.NewBrowser(configs.IsHeadless(), browser.WithBinPath(configs.GetBinPath()))
}

func saveCookies(page *rod.Page) error {
	cks, err := page.Browser().GetCookies()
	if err != nil {
		return err
	}

	data, err := json.Marshal(cks)
	if err != nil {
		return err
	}

	cookieLoader := cookies.NewLoadCookie(cookies.GetCookiesFilePath())
	return cookieLoader.SaveCookies(data)
}

// withBrowserPage 执行需要浏览器页面的操作的通用函数
func withBrowserPage(fn func(*rod.Page) error) error {
	b := newBrowser()
	defer b.Close()

	page := b.NewPage()
	defer page.Close()

	return fn(page)
}

// GetMyProfile 获取当前登录用户的个人信息
func (s *XiaohongshuService) GetMyProfile(ctx context.Context) (*UserProfileResponse, error) {
	var result *xiaohongshu.UserProfileResponse
	var err error

	err = withBrowserPage(func(page *rod.Page) error {
		action := xiaohongshu.NewUserProfileAction(page)
		result, err = action.GetMyProfileViaSidebar(ctx)
		return err
	})

	if err != nil {
		return nil, err
	}

	response := &UserProfileResponse{
		UserBasicInfo: result.UserBasicInfo,
		Interactions:  result.Interactions,
		Feeds:         result.Feeds,
	}

	return response, nil
}
