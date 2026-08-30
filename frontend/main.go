package main

import (
	"embed"

	"log"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// Wails uses Go's `embed` package to embed the frontend files into the binary.
// Any files in the frontend/dist folder will be embedded into the binary and
// made available to the frontend.
// See https://pkg.go.dev/embed for more information.

//go:embed all:frontend/dist
var assets embed.FS

func init() {
	// Register a custom event whose associated data type is string.
	// This is not required, but the binding generator will pick up registered events
	// and provide a strongly typed JS/TS API for them.
	application.RegisterEvent[string]("time")
}

// main function serves as the application's entry point. It initializes the application, creates a window,
// and starts a goroutine that emits a time-based event every second. It subsequently runs the application and
// logs any error that might occur.
func main() {

	// Create a new Wails application by providing the necessary options.
	// Variables 'Name' and 'Description' are for application metadata.
	// 'Assets' configures the asset server with the 'FS' variable pointing to the frontend files.
	// 'Bind' is a list of Go struct instances. The frontend has access to the methods of these instances.
	// 'Mac' options tailor the application when running an macOS.
	// One shared wallet session for both services: the send pipeline must
	// see the SAME unlock state as the lifecycle service.
	// 两个服务共享同一钱包会话：发送链路必须看到与生命周期服务相同的解锁状态。
	walletSvc := newWalletService()

	app := application.New(application.Options{
		Name:        "ini-node",
		Description: "A demo of using raw HTML & CSS",
		Services: []application.Service{
			application.NewService(&GreetService{}),
			// Local wallet lifecycle: create/unlock/login/lock/new-address
			// work in-process, independent of the node RPC.
			// 本地钱包生命周期：创建/解锁/登录/锁定/新地址在进程内完成，
			// 不依赖节点 RPC。
			application.NewService(walletSvc),
			// Send pipeline (Step 8): UTXO query → coin selection →
			// build+sign → broadcast, sharing the wallet session above.
			// 发送链路（第 8 步）：UTXO 查询 → 选币 → 构造+签名 → 广播，
			// 与上方钱包共享同一会话。
			application.NewService(newSendService(walletSvc)),
		},
		Assets: application.AssetOptions{
			Handler:    application.AssetFileServerFS(assets),
			Middleware: rpcProxyMiddleware(),
		},
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: true,
		},
	})

	// Create a new window with the necessary options.
	// 'Title' is the title of the window.
	// 'Mac' options tailor the window when running on macOS.
	// 'BackgroundColour' is the background colour of the window.
	// 'URL' is the URL that will be loaded into the webview.
	app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title: "Window 1",
		// Window sized to the golden ratio (1000 / 618 ≈ 1.618).
		Width:  1000,
		Height: 618,
		// Frameless removes the OS title bar; the UI provides its own
		// drag region (-webkit-app-region: drag) and window controls.
		Frameless: true,
		Windows: application.WindowsWindow{
			// Enables non-client region tracking so the frameless titlebar's
			// --wails-non-client-region: caption/minimize/maximize/close CSS
			// regions are reported for native hit testing (drag, window
			// controls). Without this the custom titlebar cannot be dragged.
			WebView2CompositionHosting: true,
		},
		Mac: application.MacWindow{
			InvisibleTitleBarHeight: 50,
			Backdrop:                application.MacBackdropTranslucent,
			TitleBar:                application.MacTitleBarHiddenInset,
		},
		BackgroundColour: application.NewRGB(6, 7, 15),
		URL:              "/",
	})

	// Create a goroutine that emits an event containing the current time every second.
	// The frontend can listen to this event and update the UI accordingly.
	go func() {
		for {
			now := time.Now().Format(time.RFC1123)
			app.Event.Emit("time", now)
			time.Sleep(time.Second)
		}
	}()

	// Run the application. This blocks until the application has been exited.
	err := app.Run()

	// If an error occurred while running the application, log it and exit.
	if err != nil {
		log.Fatal(err)
	}
}
