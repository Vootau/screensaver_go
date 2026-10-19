package main

import (
	"fmt"
	"log"
	"sort"
	"syscall"
	"unsafe"

	"github.com/hajimehoshi/ebiten/v2"
)

// ---------- WinAPI ----------

var (
	user32                  = syscall.NewLazyDLL("user32.dll")
	procEnumDisplayMonitors = user32.NewProc("EnumDisplayMonitors")
	procGetMonitorInfoW     = user32.NewProc("GetMonitorInfoW")
	procGetSystemMetrics    = user32.NewProc("GetSystemMetrics")
	procSetWindowPos        = user32.NewProc("SetWindowPos")
)

type RECT struct {
	Left, Top, Right, Bottom int32
}

type MONITORINFO struct {
	CbSize    uint32
	RcMonitor RECT
	RcWork    RECT
	DwFlags   uint32
}

type Monitor struct {
	X, Y          int32
	Width, Height int32
	IsPrimary     bool
}

func enumerateMonitors() ([]Monitor, error) {
	var monitors []Monitor

	cb := syscall.NewCallback(func(
		hMonitor uintptr, hdc uintptr, lprc *RECT, lparam uintptr,
	) uintptr {
		var info MONITORINFO
		info.CbSize = uint32(unsafe.Sizeof(info))
		ret, _, _ := procGetMonitorInfoW.Call(hMonitor, uintptr(unsafe.Pointer(&info)))
		if ret == 0 {
			return 1
		}
		monitors = append(monitors, Monitor{
			X:         info.RcMonitor.Left,
			Y:         info.RcMonitor.Top,
			Width:     info.RcMonitor.Right - info.RcMonitor.Left,
			Height:    info.RcMonitor.Bottom - info.RcMonitor.Top,
			IsPrimary: info.DwFlags&1 != 0,
		})
		return 1
	})

	ret, _, err := procEnumDisplayMonitors.Call(0, 0, cb, 0)
	if ret == 0 {
		return nil, err
	}

	sort.Slice(monitors, func(i, j int) bool {
		return monitors[i].X < monitors[j].X
	})
	return monitors, nil
}

func virtualDesktopBounds() (x, y, width, height int32) {
	call := func(index int) int32 {
		ret, _, _ := procGetSystemMetrics.Call(uintptr(index))
		return int32(ret)
	}
	return call(76), call(77), call(78), call(79)
}

// ---------- Ebitengine ----------

// Game — состояние скринсейвера.
type Game struct {
	// Пока пусто. Позже сюда добавим колонки символов, маску текста, вспышки.
}

func (g *Game) Update() error {
	return nil
}

func (g *Game) Draw(screen *ebiten.Image) {
	// Пока просто чёрный экран — Ebitengine по умолчанию не очищает его,
	// но экран и так чёрный.
}

func (g *Game) Layout(outsideWidth, outsideHeight int) (int, int) {
	return outsideWidth, outsideHeight
}

func main() {
	monitors, err := enumerateMonitors()
	if err != nil {
		log.Fatal("Не удалось получить мониторы:", err)
	}
	for _, m := range monitors {
		fmt.Printf("Монитор: %dx%d @ (%d,%d)\n", m.Width, m.Height, m.X, m.Y)
	}

	vx, vy, vw, vh := virtualDesktopBounds()
	fmt.Printf("Виртуальный стол: %dx%d @ (%d,%d)\n", vw, vh, vx, vy)

	// Создаём окно без рамки, на весь виртуальный стол.
	ebiten.SetWindowSize(int(vw), int(vh))
	ebiten.SetWindowTitle("Matrix Screensaver")
	ebiten.SetWindowDecorated(false) // без рамки, заголовка и кнопок
	ebiten.SetWindowResizingMode(ebiten.WindowResizingModeDisabled)

	// Запускаем игровой цикл в отдельной горутине? Нет — RunGame блокирующий.
	// Поэтому позиционируем окно ПОСЛЕ его создания, но ДО первого кадра.
	// Ebitengine создаёт окно при вызове RunGame, поэтому используем хак:
	// запускаем RunGame в горутине, ждём появления окна и двигаем его.
	go func() {
		if err := ebiten.RunGame(&Game{}); err != nil {
			log.Fatal(err)
		}
	}()

	// Ждём, пока окно создастся, и позиционируем его на виртуальном столе.
	// Хак: опрашиваем HWND через GetActiveWindow до победного.
	moveWindowToVirtualDesktop(vx, vy, vw, vh)

	select {} // блокируем main, чтобы программа не завершилась
}

// moveWindowToVirtualDesktop ждёт появления окна Ebitengine и двигает его
// в координаты виртуального стола. Работает через user32!SetWindowPos.
func moveWindowToVirtualDesktop(x, y, w, h int32) {
	procGetActiveWindow := user32.NewProc("GetActiveWindow")

	var hwnd uintptr
	for i := 0; i < 1000; i++ { // ждём до ~10 секунд
		hwnd, _, _ = procGetActiveWindow.Call()
		if hwnd != 0 {
			break
		}
		sleepMs(10)
	}
	if hwnd == 0 {
		log.Println("Не удалось дождаться окна")
		return
	}

	// SWP_NOZORDER=4, SWP_NOACTIVATE=16, SWP_SHOWWINDOW=64
	// HWND_TOP=0, x, y, cx, cy, flags
	const (
		SWP_NOZORDER   = 0x0004
		SWP_NOACTIVATE = 0x0010
		SWP_SHOWWINDOW = 0x0040
	)
	ret, _, err := procSetWindowPos.Call(
		hwnd, 0,
		uintptr(x), uintptr(y),
		uintptr(w), uintptr(h),
		SWP_NOZORDER|SWP_NOACTIVATE|SWP_SHOWWINDOW,
	)
	if ret == 0 {
		log.Println("SetWindowPos failed:", err)
	} else {
		fmt.Printf("Окно установлено в (%d,%d) %dx%d\n", x, y, w, h)
	}
}

// sleepMs — простой sleep через WinAPI Sleep.
func sleepMs(ms int) {
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	sleep := kernel32.NewProc("Sleep")
	sleep.Call(uintptr(ms))
}
