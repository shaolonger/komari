package javascript

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/dop251/goja"
	"github.com/komari-monitor/komari/database/models"
	"github.com/komari-monitor/komari/utils/messageSender/factory"
)

const javascriptExecutionTimeout = 30 * time.Second

type JavaScriptSender struct {
	Addition
	vm          *goja.Runtime
	noopProgram *goja.Program
	mu          sync.Mutex
}

func (j *JavaScriptSender) GetName() string {
	return "Javascript"
}

func (j *JavaScriptSender) GetConfiguration() factory.Configuration {
	return &j.Addition
}

func (j *JavaScriptSender) Init() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.initLocked()
}

func (j *JavaScriptSender) initLocked() error {
	if j.Addition.Script == "" {
		return errors.New("JavaScript script is empty")
	}

	vm := goja.New()
	j.vm = vm

	prog, err := goja.Compile("noop.js", "void 0", false)
	if err == nil {
		j.noopProgram = prog
	}

	j.setupGlobals()

	if _, err := j.vm.RunString(j.Addition.Script); err != nil {
		j.vm = nil
		return fmt.Errorf("failed to load JavaScript script: %v", err)
	}

	sendMessage := j.vm.Get("sendMessage")
	if sendMessage == nil || goja.IsUndefined(sendMessage) {
		j.vm = nil
		return errors.New("sendMessage function not defined in script")
	}
	if _, ok := goja.AssertFunction(sendMessage); !ok {
		j.vm = nil
		return errors.New("sendMessage is not a function")
	}

	return nil
}

func (j *JavaScriptSender) Destroy() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.vm != nil {
		j.vm = nil
	}
	return nil
}

func (j *JavaScriptSender) SendTextMessage(message, title string) error {
	j.mu.Lock()
	defer j.mu.Unlock()

	if j.vm == nil {
		if err := j.initLocked(); err != nil {
			return err
		}
	}

	sendMessageFunc, ok := goja.AssertFunction(j.vm.Get("sendMessage"))
	if !ok {
		return errors.New("sendMessage is not a callable function")
	}

	result, err := j.callJavaScriptLocked(sendMessageFunc, j.vm.ToValue(message), j.vm.ToValue(title))
	if err != nil {
		return err
	}
	return j.resultToErrorLocked(result, "sendMessage")
}

func (j *JavaScriptSender) SendEvent(event models.EventMessage) error {
	j.mu.Lock()

	if j.vm == nil {
		if err := j.initLocked(); err != nil {
			j.mu.Unlock()
			return err
		}
	}

	sendEventValue := j.vm.Get("sendEvent")
	if sendEventValue == nil || goja.IsUndefined(sendEventValue) {
		j.mu.Unlock()
		return j.fallbackToTextMessage(event)
	}

	sendEventFunc, ok := goja.AssertFunction(sendEventValue)
	if !ok {
		j.mu.Unlock()
		return j.fallbackToTextMessage(event)
	}

	eventJSON, err := json.Marshal(event)
	if err != nil {
		j.mu.Unlock()
		return fmt.Errorf("failed to marshal event: %v", err)
	}
	var eventMap map[string]interface{}
	if err := json.Unmarshal(eventJSON, &eventMap); err != nil {
		j.mu.Unlock()
		return fmt.Errorf("failed to unmarshal event: %v", err)
	}

	result, err := j.callJavaScriptLocked(sendEventFunc, j.vm.ToValue(eventMap))
	if err != nil {
		j.mu.Unlock()
		return err
	}
	err = j.resultToErrorLocked(result, "sendEvent")
	j.mu.Unlock()
	return err
}

func (j *JavaScriptSender) callJavaScriptLocked(fn goja.Callable, args ...goja.Value) (goja.Value, error) {
	timer := time.AfterFunc(javascriptExecutionTimeout, func() {
		j.vm.Interrupt("JavaScript execution timeout")
	})
	defer timer.Stop()

	result, err := fn(goja.Undefined(), args...)
	if err != nil {
		return nil, fmt.Errorf("JavaScript error: %v", err)
	}
	return result, nil
}

func (j *JavaScriptSender) resultToErrorLocked(result goja.Value, functionName string) error {
	if result == nil || goja.IsUndefined(result) || goja.IsNull(result) {
		return errors.New(functionName + " returned empty result")
	}

	if promise, ok := result.Export().(*goja.Promise); ok {
		deadline := time.Now().Add(javascriptExecutionTimeout)
		for {
			j.runMicrotasks()
			switch promise.State() {
			case goja.PromiseStateFulfilled:
				if promise.Result().ToBoolean() {
					return nil
				}
				return errors.New(functionName + " returned false")
			case goja.PromiseStateRejected:
				return fmt.Errorf("Promise rejected: %v", promise.Result())
			}
			if time.Now().After(deadline) {
				return errors.New("JavaScript execution timeout")
			}
			time.Sleep(10 * time.Millisecond)
		}
	}

	if result.ToBoolean() {
		return nil
	}
	return errors.New(functionName + " returned false")
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
		if delay < 0 {
			delay = 0
		}
		if delay > 5000 {
			delay = 5000
		}
		if delay > 0 {
			time.Sleep(time.Duration(delay) * time.Millisecond)
		}
		if fn, ok := goja.AssertFunction(callback); ok {
			_, _ = fn(goja.Undefined())
		}

		return goja.Undefined()
	})
	j.vm.Set("clearTimeout", func(goja.FunctionCall) goja.Value {
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
