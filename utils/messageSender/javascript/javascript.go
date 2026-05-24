package javascript

import (
	"errors"
	"fmt"
	"time"

	"github.com/dop251/goja"
	"github.com/komari-monitor/komari/database/models"
	"github.com/komari-monitor/komari/utils/messageSender/factory"
)

type JavaScriptSender struct {
	Addition
	vm          *goja.Runtime
	noopProgram *goja.Program
}

func (j *JavaScriptSender) GetName() string {
	return "Javascript"
}

func (j *JavaScriptSender) GetConfiguration() factory.Configuration {
	return &j.Addition
}

func (j *JavaScriptSender) Init() error {
	return errors.New("JavaScript notification sender is disabled for security reasons")
}

func (j *JavaScriptSender) Destroy() error {
	if j.vm != nil {
		j.vm = nil
	}
	return nil
}

func (j *JavaScriptSender) SendTextMessage(message, title string) error {
	return errors.New("JavaScript notification sender is disabled for security reasons")
}

func (j *JavaScriptSender) SendEvent(event models.EventMessage) error {
	return errors.New("JavaScript notification sender is disabled for security reasons")
}

// fallbackToTextMessage 当没有定义 sendEvent 时,回退到使用文本消息格式
func (j *JavaScriptSender) fallbackToTextMessage(event models.EventMessage) error {
	// 构建简单的文本消息
	message := fmt.Sprintf("%s%s%s\nEvent: %s\nMessage: %s\nTime: %s",
		event.Emoji, event.Emoji, event.Emoji,
		event.Event,
		event.Message,
		event.Time.Format(time.RFC3339))

	// 添加客户端信息
	if len(event.Clients) > 0 {
		clientNames := make([]string, 0, len(event.Clients))
		for _, c := range event.Clients {
			name := c.Name
			if name == "" {
				name = c.UUID
			}
			clientNames = append(clientNames, name)
		}
		message = fmt.Sprintf("%s%s%s\nEvent: %s\nClients: %s\nMessage: %s\nTime: %s",
			event.Emoji, event.Emoji, event.Emoji,
			event.Event,
			clientNames,
			event.Message,
			event.Time.Format(time.RFC3339))
	}

	return j.SendTextMessage(message, event.Event)
}

// runMicrotasks 安全地推动 goja 的微任务队列(例如 Promise 回调)
func (j *JavaScriptSender) runMicrotasks() {
	if j.vm == nil {
		return
	}
	if j.noopProgram != nil {
		_, _ = j.vm.RunProgram(j.noopProgram)
		return
	}
	// 兜底: 直接运行一段 no-op 代码
	_, _ = j.vm.RunString("void 0")
}

func (j *JavaScriptSender) setupGlobals() {
	// 注入 console.log
	console := j.vm.NewObject()
	console.Set("log", func(call goja.FunctionCall) goja.Value {
		var args []interface{}
		for _, arg := range call.Arguments {
			args = append(args, arg.Export())
		}
		fmt.Println(args...)
		return goja.Undefined()
	})
	console.Set("error", func(call goja.FunctionCall) goja.Value {
		fmt.Print("Error: ")
		for i, arg := range call.Arguments {
			if i > 0 {
				fmt.Print(" ")
			}
			fmt.Print(arg.Export())
		}
		fmt.Println()
		return goja.Undefined()
	})
	j.vm.Set("console", console)

	// 注入 fetch API
	j.vm.Set("fetch", j.createFetchFunction())

	// 注入 XMLHttpRequest (xhr)
	j.vm.Set("XMLHttpRequest", j.createXHRConstructor())

	// 注入 setTimeout
	j.vm.Set("setTimeout", func(call goja.FunctionCall) goja.Value {
		callback := call.Argument(0)
		delay := call.Argument(1).ToInteger()

		go func() {
			time.Sleep(time.Duration(delay) * time.Millisecond)
			if fn, ok := goja.AssertFunction(callback); ok {
				fn(goja.Undefined())
			}
		}()

		return goja.Undefined()
	})

	// 注入 Promise 构造函数
	j.vm.RunString(`
		if (typeof Promise === 'undefined') {
			// Promise polyfill 会由 goja 自动提供
		}
	`)
}

func init() {
	factory.RegisterMessageSender(func() factory.IMessageSender {
		return &JavaScriptSender{}
	})
}

// 确保实现了 IMessageSender 接口
var _ factory.IMessageSender = (*JavaScriptSender)(nil)
