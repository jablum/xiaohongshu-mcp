package xiaohongshu

import (
	"context"
	"time"

	"github.com/go-rod/rod"
	"github.com/sirupsen/logrus"
)

type NavigateAction struct {
	page *rod.Page
}

func NewNavigate(page *rod.Page) *NavigateAction {
	return &NavigateAction{page: page}
}

func (n *NavigateAction) ToExplorePage(ctx context.Context) error {
	page := n.page.Context(ctx)

	page.MustNavigate("https://www.xiaohongshu.com/explore").
		MustWaitLoad().
		MustElement(`div#app`)

	return nil
}

func (n *NavigateAction) ToProfilePage(ctx context.Context) error {
	page := n.page.Context(ctx)

	if err := n.ToExplorePage(ctx); err != nil {
		return err
	}

	// MustElement 自带等待机制，元素出现即返回，无需 MustWaitStable
	// explore 页面有持续动态内容，MustWaitStable 会长时间阻塞
	logrus.Info("[导航] 等待侧边栏「我」出现...")
	profileLink := page.MustElement(`div.main-container li.user.side-bar-component a.link-wrapper span.channel`)
	time.Sleep(1 * time.Second)

	logrus.Info("[导航] 点击侧边栏「我」...")
	profileLink.MustClick()

	page.MustWaitLoad()
	logrus.Info("[导航] 个人主页已加载")

	return nil
}
