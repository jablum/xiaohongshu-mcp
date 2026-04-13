package xiaohongshu

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/go-rod/rod"
	"github.com/sirupsen/logrus"
)

// CollectAction 负责获取用户收藏列表
type CollectAction struct {
	page *rod.Page
}

func NewCollectAction(page *rod.Page) *CollectAction {
	pp := page.Timeout(60 * time.Second)
	return &CollectAction{page: pp}
}

// collectAPIResponse 收藏列表API原始响应（内部解析用）
type collectAPIResponse struct {
	Code    int                `json:"code"`
	Success bool               `json:"success"`
	Msg     string             `json:"msg"`
	Data    CollectedNotesData `json:"data"`
}

// GetCollectedNotes 获取当前登录用户的收藏列表
func (c *CollectAction) GetCollectedNotes(ctx context.Context) (*CollectedNotesData, error) {
	page := c.page.Context(ctx)

	// 步骤1: 复用 NavigateAction 导航到个人主页
	logrus.Info("[收藏] 步骤1: 导航到个人主页...")
	navigate := NewNavigate(page)
	if err := navigate.ToProfilePage(ctx); err != nil {
		return nil, fmt.Errorf("导航到个人主页失败，请确认已登录: %w", err)
	}
	time.Sleep(2 * time.Second)

	urlResult, err := page.Eval(`() => window.location.href`)
	if err != nil {
		return nil, fmt.Errorf("获取当前页面URL失败: %w", err)
	}
	logrus.Infof("[收藏] 已进入个人主页: %s", urlResult.Value.String())

	// 步骤2: 注入 JS 拦截器，在页面内捕获收藏 API 响应
	logrus.Info("[收藏] 步骤2: 注入请求拦截器...")
	if err := c.injectResponseInterceptor(page); err != nil {
		return nil, err
	}

	// 步骤3: 点击收藏标签
	logrus.Info("[收藏] 步骤3: 点击收藏标签...")
	if err := c.clickCollectTab(page); err != nil {
		return nil, err
	}

	// 步骤4: 轮询等待拦截到的响应数据
	logrus.Info("[收藏] 步骤4: 等待收藏列表数据...")
	body, err := c.waitForCollectResponse(page, 15*time.Second)
	if err != nil {
		return nil, err
	}

	logrus.Infof("[收藏] 获取到响应数据，长度=%d", len(body))
	return c.parseCollectResponse(body)
}

// injectResponseInterceptor 注入 JS 代码，拦截 fetch/XHR 中收藏列表 API 的响应
func (c *CollectAction) injectResponseInterceptor(page *rod.Page) error {
	_, err := page.Eval(`() => {
		window.__collectResponse = null;

		// 拦截 fetch
		const origFetch = window.fetch;
		window.fetch = function(...args) {
			const promise = origFetch.apply(this, args);
			const url = typeof args[0] === 'string' ? args[0] : (args[0] && args[0].url) || '';
			if (url.includes('note/collect/page')) {
				promise.then(resp => resp.clone().text()).then(text => {
					window.__collectResponse = text;
				}).catch(() => {});
			}
			return promise;
		};

		// 拦截 XMLHttpRequest
		const origOpen = XMLHttpRequest.prototype.open;
		XMLHttpRequest.prototype.open = function(method, url) {
			this.__url = url;
			return origOpen.apply(this, arguments);
		};
		const origSend = XMLHttpRequest.prototype.send;
		XMLHttpRequest.prototype.send = function() {
			if (this.__url && this.__url.includes('note/collect/page')) {
				this.addEventListener('load', function() {
					window.__collectResponse = this.responseText;
				});
			}
			return origSend.apply(this, arguments);
		};
	}`)
	if err != nil {
		return fmt.Errorf("注入请求拦截器失败: %w", err)
	}
	logrus.Info("[收藏] 请求拦截器已注入")
	return nil
}

// waitForCollectResponse 轮询读取页面中被拦截到的收藏 API 响应
func (c *CollectAction) waitForCollectResponse(page *rod.Page, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		res, err := page.Eval(`() => window.__collectResponse || ""`)
		if err != nil {
			return "", fmt.Errorf("读取拦截响应失败: %w", err)
		}
		if result := res.Value.String(); result != "" {
			return result, nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return "", fmt.Errorf("等待收藏列表API响应超时（%v）", timeout)
}

// clickCollectTab 点击用户主页的"收藏"标签
func (c *CollectAction) clickCollectTab(page *rod.Page) error {
	// 打印页面上 tab 信息用于排查
	tabInfoRes, err := page.Eval(`() => {
		const result = [];
		const selectors = ['.user-tab .tab', '.tabs .tab-item', '.reds-tab-item', '[class*="tab"]'];
		for (const selector of selectors) {
			const tabs = document.querySelectorAll(selector);
			for (const tab of tabs) {
				const text = tab.textContent.trim();
				if (text.length > 0 && text.length < 20) {
					result.push({selector: selector, text: text, className: tab.className});
				}
			}
		}
		return JSON.stringify(result);
	}`)
	if err != nil {
		return fmt.Errorf("获取页面 tab 元素失败: %w", err)
	}
	logrus.Infof("[收藏] 页面 tab 元素: %s", tabInfoRes.Value.String())

	// 查找包含"收藏"文本的标签并点击
	clickedRes, err := page.Eval(`() => {
		const selectors = ['.user-tab .tab', '.tabs .tab-item', '.reds-tab-item', '[class*="tab"]'];
		for (const selector of selectors) {
			const tabs = document.querySelectorAll(selector);
			for (const tab of tabs) {
				const text = tab.textContent.trim();
				if (text.includes('收藏') && !text.includes('获赞与收藏')) {
					tab.click();
					return true;
				}
			}
		}
		return false;
	}`)
	if err != nil {
		return fmt.Errorf("执行点击收藏标签脚本失败: %w", err)
	}

	if !clickedRes.Value.Bool() {
		return fmt.Errorf("未找到收藏标签，请确认当前页面为个人主页")
	}

	logrus.Info("[收藏] 已点击收藏标签，等待数据加载...")
	time.Sleep(2 * time.Second)
	return nil
}

// parseCollectResponse 解析收藏列表 API 响应
func (c *CollectAction) parseCollectResponse(body string) (*CollectedNotesData, error) {
	var resp collectAPIResponse
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		logrus.Errorf("[收藏] JSON解析失败, body前200字符: %.200s", body)
		return nil, fmt.Errorf("解析收藏列表响应失败: %w", err)
	}

	if !resp.Success || resp.Code != 0 {
		return nil, fmt.Errorf("获取收藏列表失败: code=%d, msg=%s", resp.Code, resp.Msg)
	}

	logrus.Infof("[收藏] 成功获取 %d 条收藏笔记, has_more=%v, cursor=%s",
		len(resp.Data.Notes), resp.Data.HasMore, resp.Data.Cursor)
	return &resp.Data, nil
}
