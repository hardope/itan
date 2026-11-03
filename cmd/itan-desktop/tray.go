package main

import (
	_ "embed"
	"log"
	"runtime"
	"slices"
	"sync"
	"time"

	"fyne.io/systray"

	"github.com/hardope/itan/internal/app"
)

var (
	//go:embed icons/tray-template.png
	trayTemplate []byte
	//go:embed icons/tray.ico
	trayICO []byte
)

// maxAddresses is how many network addresses the menu has room for.
const maxAddresses = 4

// trayReady builds the menu:
//
//	Itan 0.2.0 is running
//	Open Itan
//	Watch on other devices ▸ (addresses; click one to copy it)
//	────────────
//	✓ Open at login
//	────────────
//	Quit Itan
func trayReady(a *app.App) {
	if runtime.GOOS == "darwin" {
		systray.SetTemplateIcon(trayTemplate, trayTemplate)
	} else {
		systray.SetIcon(trayICO)
	}
	systray.SetTooltip("Itan")

	status := systray.AddMenuItem("Itan "+version+" is running", "")
	status.Disable()
	open := systray.AddMenuItem("Open Itan", "Open Itan in your browser")

	devices := systray.AddMenuItem("Watch on other devices", "Addresses for phones, tablets and TVs on this network")
	hint := devices.AddSubMenuItem("Click an address to copy it", "")
	hint.Disable()
	var addrs [maxAddresses]*systray.MenuItem
	for i := range addrs {
		addrs[i] = devices.AddSubMenuItem("", "Copy this address")
		addrs[i].Hide()
	}
	var mu sync.Mutex
	var shown []string
	showAddresses := func() {
		urls := a.LANURLs()
		if len(urls) > maxAddresses {
			urls = urls[:maxAddresses]
		}
		mu.Lock()
		defer mu.Unlock()
		if slices.Equal(urls, shown) && shown != nil {
			return
		}
		shown = urls
		if len(urls) == 0 {
			hint.SetTitle("Not connected to a network")
		} else {
			hint.SetTitle("Click an address to copy it")
		}
		for i, item := range addrs {
			if i < len(urls) {
				item.SetTitle(urls[i])
				item.Show()
			} else {
				item.Hide()
			}
		}
	}
	showAddresses()
	go func() {
		// Wi-Fi changes and DHCP can change the address.
		for range time.Tick(30 * time.Second) {
			showAddresses()
		}
	}()
	for i, item := range addrs {
		go func() {
			for range item.ClickedCh {
				mu.Lock()
				u := ""
				if i < len(shown) {
					u = shown[i]
				}
				mu.Unlock()
				if u != "" {
					if err := copyText(u); err != nil {
						log.Printf("itan: copy: %v", err)
					}
				}
			}
		}()
	}

	systray.AddSeparator()
	var login *systray.MenuItem
	if loginItemSupported() {
		login = systray.AddMenuItemCheckbox("Open at login", "Start Itan when you log in, so other devices can watch", loginItemEnabled())
	} else if why := loginItemUnsupported(); why != "" {
		login = systray.AddMenuItem("Open at login: "+why, "")
		login.Disable()
	}
	systray.AddSeparator()
	quit := systray.AddMenuItem("Quit Itan", "Stop Itan. Other devices can't watch until it's opened again.")

	openItan := func() { openBrowser(a.LocalURL()) }
	if runtime.GOOS == "windows" {
		// Clicking the icon opens Itan; right-clicking shows the menu.
		systray.SetOnTapped(openItan)
	}
	onReopen(openItan)

	var loginClicked <-chan struct{}
	if login != nil {
		loginClicked = login.ClickedCh
	}
	for {
		select {
		case <-open.ClickedCh:
			openItan()
		case <-loginClicked:
			on := !login.Checked()
			if err := setLoginItem(on); err != nil {
				log.Printf("itan: open at login: %v", err)
				alert("Couldn't change Open at login", err.Error())
				continue
			}
			if on {
				login.Check()
			} else {
				login.Uncheck()
			}
		case <-quit.ClickedCh:
			systray.Quit()
			return
		}
	}
}
