//go:build js

package main

import (
	"io"
	"syscall/js"

	"github.com/DanBradbury/firekeeper/internal/tui"
	tea "github.com/charmbracelet/bubbletea"
)

type browserOutput struct{}

func (browserOutput) Write(data []byte) (int, error) {
	buffer := js.Global().Get("Uint8Array").New(len(data))
	js.CopyBytesToJS(buffer, data)
	js.Global().Call("firekeeperOutput", buffer)
	return len(data), nil
}

func main() {
	reader, writer := io.Pipe()
	inputData := make(chan string, 64)
	go func() {
		for data := range inputData {
			if _, err := io.WriteString(writer, data); err != nil {
				return
			}
		}
	}()
	program := tea.NewProgram(tui.NewDemoModel(), tea.WithInput(reader), tea.WithOutput(browserOutput{}), tea.WithAltScreen(), tea.WithoutSignalHandler())
	input := js.FuncOf(func(this js.Value, args []js.Value) any {
		if len(args) > 0 {
			select {
			case inputData <- args[0].String():
			default:
			}
		}
		return nil
	})
	resize := js.FuncOf(func(this js.Value, args []js.Value) any {
		if len(args) >= 2 {
			go program.Send(tea.WindowSizeMsg{Width: args[0].Int(), Height: args[1].Int()})
		}
		return nil
	})
	js.Global().Set("firekeeperInput", input)
	js.Global().Set("firekeeperResize", resize)
	_, _ = program.Run()
	js.Global().Set("firekeeperInput", js.Undefined())
	js.Global().Set("firekeeperResize", js.Undefined())
	_ = writer.Close()
	close(inputData)
	input.Release()
	resize.Release()
}
