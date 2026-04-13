package xiaohongshu

import (
	"context"
	"fmt"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
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

	if err := page.Navigate("https://www.xiaohongshu.com/explore"); err != nil {
		return fmt.Errorf("导航到探索页失败: %w", err)
	}
	if err := page.WaitLoad(); err != nil {
		return fmt.Errorf("等待探索页加载失败: %w", err)
	}
	if _, err := page.Element(`div#app`); err != nil {
		return fmt.Errorf("等待探索页 app 元素失败: %w", err)
	}

	return nil
}

func (n *NavigateAction) ToProfilePage(ctx context.Context) error {
	page := n.page.Context(ctx)

	if err := n.ToExplorePage(ctx); err != nil {
		return err
	}

	// Element 自带等待机制，元素出现即返回，无需 WaitStable
	// explore 页面有持续动态内容，WaitStable 会长时间阻塞
	logrus.Info("[导航] 等待侧边栏「我」出现...")
	profileLink, err := page.Element(`div.main-container li.user.side-bar-component a.link-wrapper span.channel`)
	if err != nil {
		return fmt.Errorf("等待侧边栏「我」出现失败: %w", err)
	}
	time.Sleep(1 * time.Second)

	logrus.Info("[导航] 点击侧边栏「我」...")
	if err := profileLink.Click(proto.InputMouseButtonLeft, 1); err != nil {
		return fmt.Errorf("点击侧边栏「我」失败: %w", err)
	}

	if err := page.WaitLoad(); err != nil {
		return fmt.Errorf("等待个人主页加载失败: %w", err)
	}
	logrus.Info("[导航] 个人主页已加载")

	return nil
}
