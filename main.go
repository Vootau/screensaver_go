package main

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"log"
	"math"
	"math/rand"
	"os"
	"sort"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/inpututil"
	"github.com/hajimehoshi/ebiten/v2/text/v2"
	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

// ---------- WinAPI ----------

var (
	user32                  = syscall.NewLazyDLL("user32.dll")
	procEnumDisplayMonitors = user32.NewProc("EnumDisplayMonitors")
	procGetMonitorInfoW     = user32.NewProc("GetMonitorInfoW")
	procGetSystemMetrics    = user32.NewProc("GetSystemMetrics")
	procSetWindowPos        = user32.NewProc("SetWindowPos")
)

type RECT struct{ Left, Top, Right, Bottom int32 }

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
	cb := syscall.NewCallback(func(hMonitor, hdc uintptr, lprc *RECT, lparam uintptr) uintptr {
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
	sort.Slice(monitors, func(i, j int) bool { return monitors[i].X < monitors[j].X })
	return monitors, nil
}

func virtualDesktopBounds() (x, y, width, height int32) {
	call := func(index int) int32 {
		ret, _, _ := procGetSystemMetrics.Call(uintptr(index))
		return int32(ret)
	}
	return call(76), call(77), call(78), call(79)
}

// ---------- Константы ----------

const (
	fontPath     = `C:\Windows\Fonts\consola.ttf`
	rainFontSize = 12 // дождь снаружи маски
	maskFontSize = 32 // дождь внутри маски (выпуклая линза)
	cellW        = 10
	cellH        = 17
	textFontSize = 250 // только для растеризации маски
)

// ---------- Символы ----------

var matrixChars = func() []string {
	s := "アイウエオカキクケコサシスセソタチツテトナニヌネノハヒフヘホマミムメモヤユヨラリルレロワヲン0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	result := make([]string, 0, len(s))
	for _, r := range s {
		result = append(result, string(r))
	}
	return result
}()

func randomChar() string {
	return matrixChars[rand.Intn(len(matrixChars))]
}

// ---------- Column ----------

type Column struct {
	X      float64
	Y      float64
	Speed  float64
	Length int
	Chars  []string
}

func newColumn(x float64, screenHeight int) *Column {
	c := &Column{X: x}
	c.reset(screenHeight, true)
	return c
}

func (c *Column) reset(screenHeight int, initial bool) {
	// Скорость: экспоненциальное распределение.
	// Большинство попадает в 0.5..4, но иногда выпадают 10, 15, 20+.
	c.Speed = math.Min(0.5+rand.ExpFloat64()*2.5, 12.0)

	// Длина хвоста зависит от скорости.
	switch {
	case c.Speed < 1.0:
		// Медленные — длинные тусклые «сосульки».
		c.Length = 25 + rand.Intn(20) // 25..44
	case c.Speed > 8.0:
		// Быстрые — короткие яркие «искры».
		c.Length = 6 + rand.Intn(6) // 6..11
	default:
		// Обычные.
		c.Length = 15 + rand.Intn(15) // 15..29
	}

	c.Chars = make([]string, c.Length)
	for i := range c.Chars {
		c.Chars[i] = randomChar()
	}

	if initial {
		c.Y = rand.Float64() * float64(screenHeight)
	} else {
		c.Y = -rand.Float64() * float64(screenHeight) * 0.5
	}
}

func (c *Column) Update(screenHeight int) {
	c.Y += c.Speed
	if c.Y-float64(c.Length)*cellH > float64(screenHeight) {
		c.reset(screenHeight, false)
	}
	if rand.Float32() < 0.02 {
		c.Chars[rand.Intn(len(c.Chars))] = randomChar()
	}
}

func (c *Column) Draw(screen *ebiten.Image, rainFace, maskFace *text.GoTextFace, mask [][]bool) {
	cellsY := len(mask)
	if cellsY == 0 {
		return
	}
	cellsX := len(mask[0])

	for i := 0; i < c.Length; i++ {
		y := c.Y - float64(i)*cellH
		if y < -float64(maskFontSize) || y > float64(screen.Bounds().Dy()) {
			continue
		}

		// Центр символа в координатах экрана
		cx := int(c.X+float64(rainFontSize)/2.0) / cellW
		cy := int(y+float64(rainFontSize)/2.0) / cellH
		insideMask := cx >= 0 && cx < cellsX && cy >= 0 && cy < cellsY && mask[cy][cx]

		var face *text.GoTextFace
		var clr color.RGBA
		var offset float64

		if insideMask {
			face = maskFace
			offset = float64(maskFontSize-rainFontSize) / 2.0
			if i == 0 {
				clr = color.RGBA{200, 255, 200, 255}
			} else {
				fade := 1.0 - float64(i)/float64(c.Length)
				clr = color.RGBA{40, uint8(240*fade + 40), 40, uint8(255 * fade)}
			}
		} else {
			face = rainFace
			if i == 0 {
				clr = color.RGBA{140, 220, 140, 255}
			} else {
				fade := 1.0 - float64(i)/float64(c.Length)
				clr = color.RGBA{0, uint8(180*fade + 50), 0, uint8(255 * fade)}
			}
		}

		op := &text.DrawOptions{}
		op.GeoM.Translate(c.X-offset, y-offset)
		op.ColorScale.ScaleWithColor(clr)
		text.Draw(screen, c.Chars[i], face, op)
	}
}

// ---------- Game ----------

type Game struct {
	rainFace         *text.GoTextFace
	maskFace         *text.GoTextFace
	monitors         []Monitor
	columns          []*Column
	mask             [][]bool
	screenW, screenH int

	startTime  time.Time
	initMouse  bool
	lastMouseX int
	lastMouseY int
}

func NewGame(monitors []Monitor, screenW, screenH int) (*Game, error) {
	fontData, err := os.ReadFile(fontPath)
	if err != nil {
		return nil, fmt.Errorf("шрифт: %w", err)
	}
	src, err := text.NewGoTextFaceSource(bytes.NewReader(fontData))
	if err != nil {
		return nil, fmt.Errorf("разбор шрифта: %w", err)
	}

	mask, err := buildMask(monitors, screenW, screenH, fontData)
	if err != nil {
		return nil, fmt.Errorf("маска: %w", err)
	}

	numCols := screenW / cellW
	columns := make([]*Column, numCols)
	for i := 0; i < numCols; i++ {
		columns[i] = newColumn(float64(i*cellW), screenH)
	}

	return &Game{
		rainFace:  &text.GoTextFace{Source: src, Size: rainFontSize},
		maskFace:  &text.GoTextFace{Source: src, Size: maskFontSize},
		monitors:  monitors,
		columns:   columns,
		mask:      mask,
		screenW:   screenW,
		screenH:   screenH,
		startTime: time.Now(),
	}, nil
}

// buildMask растеризует текст и заполняет булев массив «внутри/снаружи».
func buildMask(monitors []Monitor, screenW, screenH int, fontData []byte) ([][]bool, error) {
	f, err := opentype.Parse(fontData)
	if err != nil {
		return nil, err
	}
	face, err := opentype.NewFace(f, &opentype.FaceOptions{
		Size: textFontSize, DPI: 72, Hinting: font.HintingFull,
	})
	if err != nil {
		return nil, err
	}
	defer face.Close()

	img := image.NewRGBA(image.Rect(0, 0, screenW, screenH))
	labels := []string{"THE MATRIX", "HAS YOU"}

	for i, m := range monitors {
		if i >= len(labels) {
			break
		}
		label := labels[i]
		bounds, _ := font.BoundString(face, label)
		textW := (bounds.Max.X - bounds.Min.X).Ceil()
		textH := (bounds.Max.Y - bounds.Min.Y).Ceil()

		cx := int(m.X + m.Width/2)
		cy := int(m.Y + m.Height/2)
		baselineX := cx - textW/2 - bounds.Min.X.Ceil()
		baselineY := cy - textH/2 - bounds.Min.Y.Ceil()

		d := &font.Drawer{
			Dst:  img,
			Src:  image.NewUniform(color.White),
			Face: face,
			Dot:  fixed.P(baselineX, baselineY),
		}
		d.DrawString(label)
	}

	cellsX := screenW / cellW
	cellsY := screenH / cellH
	mask := make([][]bool, cellsY)
	for y := 0; y < cellsY; y++ {
		mask[y] = make([]bool, cellsX)
		for x := 0; x < cellsX; x++ {
			px := x*cellW + cellW/2
			py := y*cellH + cellH/2
			mask[y][x] = img.RGBAAt(px, py).A > 50
		}
	}
	return mask, nil
}

func (g *Game) Update() error {
	for _, c := range g.columns {
		c.Update(g.screenH)
	}

	if time.Since(g.startTime) < 3000*time.Millisecond {
		return nil
	}
	if len(inpututil.AppendPressedKeys(nil)) > 0 {
		return ebiten.Termination
	}
	if ebiten.IsMouseButtonPressed(ebiten.MouseButtonLeft) ||
		ebiten.IsMouseButtonPressed(ebiten.MouseButtonRight) ||
		ebiten.IsMouseButtonPressed(ebiten.MouseButtonMiddle) {
		return ebiten.Termination
	}
	mx, my := ebiten.CursorPosition()
	if !g.initMouse {
		g.lastMouseX, g.lastMouseY = mx, my
		g.initMouse = true
	} else {
		dx, dy := mx-g.lastMouseX, my-g.lastMouseY
		if dx < 0 {
			dx = -dx
		}
		if dy < 0 {
			dy = -dy
		}
		if dx+dy > 10 {
			return ebiten.Termination
		}
	}
	return nil
}

func (g *Game) Draw(screen *ebiten.Image) {
	for _, c := range g.columns {
		c.Draw(screen, g.rainFace, g.maskFace, g.mask)
	}
}

func (g *Game) Layout(outsideWidth, outsideHeight int) (int, int) {
	return outsideWidth, outsideHeight
}

// ---------- main ----------

func main() {

	// Windows передаёт аргументы при запуске скринсейвера.
	if len(os.Args) > 1 {
		arg := strings.ToLower(os.Args[1])
		if strings.HasPrefix(arg, "/c") || strings.HasPrefix(arg, "-c") {
			// Режим настроек. Пока просто выходим.
			return
		}
		if strings.HasPrefix(arg, "/p") || strings.HasPrefix(arg, "-p") {
			// Режим превью. Пока просто выходим.
			// Полноценная реализация — отдельная задача.
			return
		}
		// "/s" или без аргументов — полноэкранный режим.
	}
	monitors, err := enumerateMonitors()
	if err != nil {
		log.Fatal(err)
	}
	vx, vy, vw, vh := virtualDesktopBounds()
	for i := range monitors {
		monitors[i].X -= vx
		monitors[i].Y -= vy
	}
	fmt.Printf("Виртуальный стол: %dx%d @ (%d,%d)\n", vw, vh, vx, vy)

	game, err := NewGame(monitors, int(vw), int(vh))
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("Маска построена")

	ebiten.SetWindowSize(int(vw), int(vh))
	ebiten.SetWindowTitle("Matrix Screensaver")
	ebiten.SetWindowDecorated(false)
	ebiten.SetWindowResizingMode(ebiten.WindowResizingModeDisabled)

	go moveWindowToVirtualDesktop("Matrix Screensaver", vx, vy, vw, vh)

	// Принудительно используем OpenGL — помогает стабильности в полноэкранном режиме.
	os.Setenv("EBITENGINE_GRAPHICS_LIBRARY", "opengl")

	if err := ebiten.RunGame(game); err != nil {
		log.Fatal(err)
	}
}

func moveWindowToVirtualDesktop(title string, x, y, w, h int32) {
	procFindWindowW := user32.NewProc("FindWindowW")
	titlePtr, _ := syscall.UTF16PtrFromString(title)
	var hwnd uintptr
	for i := 0; i < 1000; i++ {
		hwnd, _, _ = procFindWindowW.Call(0, uintptr(unsafe.Pointer(titlePtr)))
		if hwnd != 0 {
			break
		}
		sleepMs(10)
	}
	if hwnd == 0 {
		return
	}
	const (
		SWP_NOZORDER   = 0x0004
		SWP_NOACTIVATE = 0x0010
		SWP_SHOWWINDOW = 0x0040
	)
	procSetWindowPos.Call(hwnd, 0, uintptr(x), uintptr(y), uintptr(w), uintptr(h),
		SWP_NOZORDER|SWP_NOACTIVATE|SWP_SHOWWINDOW)
}

func sleepMs(ms int) {
	syscall.NewLazyDLL("kernel32.dll").NewProc("Sleep").Call(uintptr(ms))
}
