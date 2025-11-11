//go:build js && wasm
// +build js,wasm

package main

import (
	"encoding/json"
	"fmt"
	"image/color"
	"log"
	"math"
	"strings"
	"sync"
	"syscall/js"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/ebitenutil"
	"github.com/hajimehoshi/ebiten/v2/vector"
)

const (
	screenWidth           = 480
	screenHeight          = 640
	playerSpeed           = 3.0
	playerSize            = 10
	territoryBoundary     = screenHeight / 2
	defaultProjectileSize = 5
	maxHP                 = 100
	maxMP                 = 100
	wsPath                = "/ws"
	defaultLocalHost      = "127.0.0.1"
	defaultLocalPort      = "8080"
	defaultLocalWSURL     = "ws://" + defaultLocalHost + ":" + defaultLocalPort + wsPath
)

type Player struct {
	X  float64
	Y  float64
	HP int
	MP int
}

type Projectile struct {
	ID      string
	X       float64
	Y       float64
	VX      float64
	VY      float64
	Width   float64 // 投射物の横幅
	Height  float64 // 投射物の縦幅
	Element string
}

// WASMWebSocket JavaScriptのWebSocket APIをラップ
type WASMWebSocket struct {
	ws          js.Value
	onOpen      js.Func
	onMessage   js.Func
	onError     js.Func
	onClose     js.Func
	messageChan chan []byte
	errorChan   chan error
	closed      bool
	mutex       sync.RWMutex
}

// NewWASMWebSocket 新しいWASMWebSocketを作成
func NewWASMWebSocket(url string) (*WASMWebSocket, error) {
	ws := js.Global().Get("WebSocket").New(url)

	w := &WASMWebSocket{
		ws:          ws,
		messageChan: make(chan []byte, 100),
		errorChan:   make(chan error, 10),
		closed:      false,
	}

	// イベントハンドラーを設定
	w.onOpen = js.FuncOf(func(this js.Value, args []js.Value) interface{} {
		log.Printf("WebSocket接続成功: %s", url)
		return nil
	})

	w.onMessage = js.FuncOf(func(this js.Value, args []js.Value) interface{} {
		event := args[0]
		data := event.Get("data")
		message := []byte(data.String())
		select {
		case w.messageChan <- message:
		default:
			// チャネルが満杯の場合はスキップ
		}
		return nil
	})

	w.onError = js.FuncOf(func(this js.Value, args []js.Value) interface{} {
		event := args[0]
		errMsg := "WebSocket error"
		if !event.IsNull() && !event.IsUndefined() {
			if msg := event.Get("message"); !msg.IsUndefined() {
				errMsg = msg.String()
			}
		}
		err := fmt.Errorf(errMsg)
		select {
		case w.errorChan <- err:
		default:
		}
		log.Printf("WebSocket error: %v", err)
		return nil
	})

	w.onClose = js.FuncOf(func(this js.Value, args []js.Value) interface{} {
		w.mutex.Lock()
		w.closed = true
		w.mutex.Unlock()
		log.Printf("WebSocket closed")
		return nil
	})

	ws.Set("onopen", w.onOpen)
	ws.Set("onmessage", w.onMessage)
	ws.Set("onerror", w.onError)
	ws.Set("onclose", w.onClose)

	return w, nil
}

// Send メッセージを送信
func (w *WASMWebSocket) Send(data []byte) error {
	w.mutex.RLock()
	closed := w.closed
	w.mutex.RUnlock()

	if closed {
		return fmt.Errorf("WebSocket is closed")
	}

	// WebSocketに送信（文字列として送信）
	w.ws.Call("send", string(data))
	return nil
}

// SendJSON JSONを送信
func (w *WASMWebSocket) SendJSON(v interface{}) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return w.Send(data)
}

// ReadMessage メッセージを受信
func (w *WASMWebSocket) ReadMessage() ([]byte, error) {
	select {
	case msg := <-w.messageChan:
		return msg, nil
	case err := <-w.errorChan:
		return nil, err
	}
}

// Close 接続を閉じる
func (w *WASMWebSocket) Close() error {
	w.mutex.Lock()
	defer w.mutex.Unlock()

	if w.closed {
		return nil
	}

	w.closed = true
	w.ws.Call("close")

	// クリーンアップ
	w.onOpen.Release()
	w.onMessage.Release()
	w.onError.Release()
	w.onClose.Release()

	return nil
}

// ReadyState 接続状態を取得
func (w *WASMWebSocket) ReadyState() int {
	return w.ws.Get("readyState").Int()
}

type Game struct {
	player        Player
	conn          *WASMWebSocket
	connMutex     sync.RWMutex
	typingBuffer  string
	prevBackspace bool
	prevEnter     bool
	prevSpace     bool
	// サーバーからのメッセージ
	lastMessage  string
	messageMutex sync.RWMutex
	// 他のプレイヤー
	otherPlayers map[string]*Player
	playersMutex sync.RWMutex
	// 魔法投射物
	projectiles      map[string]*Projectile
	projectilesMutex sync.RWMutex
	// 自分のID
	myID string
	// 自分の陣地（true: 下側、false: 上側）
	isBottom bool
	// 接続状態
	connectionError string
	// 接続試行中かどうか
	connecting    bool
	serverPlayerX float64
	serverPlayerY float64
	hasServerPos  bool
}

// getWebSocketURL HTMLのmetaタグからWebSocketのURLを取得
func getWebSocketURL() string {
	// JavaScriptのDOM APIを使用してmetaタグを取得
	doc := js.Global().Get("document")
	metaWS := doc.Call("querySelector", `meta[name="ws-url"]`)

	if !metaWS.IsNull() && !metaWS.IsUndefined() {
		content := strings.TrimSpace(metaWS.Get("content").String())
		if content != "" {
			return content
		}
	}

	location := js.Global().Get("window").Get("location")
	protocol := location.Get("protocol").String()
	hostname := location.Get("hostname").String()
	port := location.Get("port").String()

	if hostname == "" {
		return defaultLocalWSURL
	}

	if hostname == "localhost" || hostname == defaultLocalHost {
		return defaultLocalWSURL
	}

	wsScheme := "ws"
	if protocol == "https:" {
		wsScheme = "wss"
	}

	if port != "" && port != "80" && port != "443" {
		return fmt.Sprintf("%s://%s:%s%s", wsScheme, hostname, port, wsPath)
	}

	return fmt.Sprintf("%s://%s%s", wsScheme, hostname, wsPath)
}

func NewGame() *Game {
	game := &Game{
		player: Player{
			X:  screenWidth / 2,
			Y:  territoryBoundary + 50,
			HP: maxHP,
			MP: maxMP,
		},
		conn:          nil,
		typingBuffer:  "",
		lastMessage:   "",
		otherPlayers:  make(map[string]*Player),
		projectiles:   make(map[string]*Projectile),
		myID:          "",
		isBottom:      true,
		serverPlayerX: screenWidth / 2,
		serverPlayerY: territoryBoundary + 50,
		hasServerPos:  true,
	}

	go game.connectWebSocket()

	return game
}

// connectWebSocket WebSocket接続を確立
func (g *Game) connectWebSocket() {
	g.connMutex.Lock()
	if g.connecting || g.conn != nil {
		g.connMutex.Unlock()
		return
	}
	g.connecting = true
	g.connMutex.Unlock()

	wsURL := getWebSocketURL()
	conn, err := NewWASMWebSocket(wsURL)
	if err != nil {
		g.connMutex.Lock()
		g.connectionError = fmt.Sprintf("WebSocket作成エラー: %v", err)
		g.connecting = false
		g.connMutex.Unlock()
		log.Printf("WebSocket作成エラー: %v", err)
		return
	}

	const (
		maxWaitTime  = 20000
		waitInterval = 100
	)
	waited := 0

	var checkConnection js.Func
	checkConnection = js.FuncOf(func(this js.Value, args []js.Value) interface{} {
		readyState := conn.ReadyState()
		const (
			stateConnecting = 0
			stateOpen       = 1
			stateClosing    = 2
			stateClosed     = 3
		)

		if readyState == stateOpen {
			g.connMutex.Lock()
			g.conn = conn
			g.connectionError = ""
			g.connecting = false
			g.connMutex.Unlock()
			log.Printf("WebSocket接続成功: %s", wsURL)

			go g.readMessages()
			checkConnection.Release()
			return nil
		} else if readyState == stateClosed {
			g.connMutex.Lock()
			g.connectionError = "接続に失敗しました"
			g.connecting = false
			g.connMutex.Unlock()
			log.Printf("WebSocket接続に失敗しました")
			conn.Close()
			checkConnection.Release()
			g.retryConnection()
			return nil
		}

		waited += waitInterval
		if waited >= maxWaitTime {
			g.connMutex.Lock()
			g.connectionError = "接続タイムアウト"
			g.connecting = false
			g.connMutex.Unlock()
			conn.Close()
			checkConnection.Release()
			g.retryConnection()
			return nil
		}

		g.connMutex.Lock()
		g.connectionError = "接続中..."
		g.connMutex.Unlock()

		js.Global().Call("setTimeout", checkConnection, waitInterval)
		return nil
	})

	js.Global().Call("setTimeout", checkConnection, waitInterval)
}

// retryConnection 接続をリトライ
func (g *Game) retryConnection() {
	js.Global().Call("setTimeout", js.FuncOf(func(this js.Value, args []js.Value) interface{} {
		g.connectWebSocket()
		return nil
	}), 1000)
}

// readMessages サーバーからのメッセージを受信
func (g *Game) readMessages() {
	g.connMutex.RLock()
	conn := g.conn
	g.connMutex.RUnlock()

	if conn == nil {
		return
	}

	for {
		message, err := conn.ReadMessage()
		if err != nil {
			log.Printf("Read error: %v", err)
			// 接続が切断された場合、再接続を試みる
			g.connMutex.Lock()
			g.conn = nil
			g.connectionError = "接続が切断されました。再接続中..."
			g.connMutex.Unlock()
			go g.connectWebSocket()
			return
		}

		// JSONメッセージを解析
		var msg map[string]interface{}
		if err := json.Unmarshal(message, &msg); err != nil {
			continue
		}

		// メッセージタイプで分岐
		if msgType, ok := msg["type"].(string); ok {
			switch msgType {
			case "init":
				if myID, ok := msg["myID"].(string); ok {
					g.myID = myID
				}
				if isBottom, ok := msg["isBottom"].(bool); ok {
					g.isBottom = isBottom
				}
			case "playerStates":
				g.handlePlayerStatesUpdate(msg)
			case "projectiles":
				g.handleProjectilesUpdate(msg)
			case "positionCorrection":
				if x, ok := msg["x"].(float64); ok {
					if y, ok := msg["y"].(float64); ok {
						g.serverPlayerX = x
						g.serverPlayerY = y
						g.hasServerPos = true
					}
				}
				continue
			}
		}

		// 魔法詠唱イベントを処理
		if castEvent, ok := msg["castEvent"].(string); ok {
			g.handleCastEvent(msg, castEvent)
		}
	}
}

// handlePlayerStatesUpdate プレイヤー状態の更新を処理
func (g *Game) handlePlayerStatesUpdate(msg map[string]interface{}) {
	states, ok := msg["states"].(map[string]interface{})
	if !ok {
		return
	}

	g.playersMutex.Lock()
	defer g.playersMutex.Unlock()

	receivedIDs := make(map[string]bool)
	for id, stateData := range states {
		receivedIDs[id] = true
		state, ok := stateData.(map[string]interface{})
		if !ok {
			continue
		}

		if g.myID != "" && id == g.myID {
			if hp, ok := state["hp"].(float64); ok {
				g.player.HP = int(hp)
			}
			if mp, ok := state["mp"].(float64); ok {
				g.player.MP = int(mp)
			}
		} else {
			if g.otherPlayers[id] == nil {
				g.otherPlayers[id] = &Player{}
			}
			if x, ok := state["x"].(float64); ok {
				g.otherPlayers[id].X = x
			}
			if y, ok := state["y"].(float64); ok {
				g.otherPlayers[id].Y = y
			}
			if hp, ok := state["hp"].(float64); ok {
				g.otherPlayers[id].HP = int(hp)
			}
			if mp, ok := state["mp"].(float64); ok {
				g.otherPlayers[id].MP = int(mp)
			}
		}
	}

	for id := range g.otherPlayers {
		if id != g.myID && !receivedIDs[id] {
			delete(g.otherPlayers, id)
		}
	}
}

// handleProjectilesUpdate 投射物の更新を処理
func (g *Game) handleProjectilesUpdate(msg map[string]interface{}) {
	projs, ok := msg["projectiles"].([]interface{})
	if !ok {
		return
	}

	g.projectilesMutex.Lock()
	defer g.projectilesMutex.Unlock()

	projIDs := make(map[string]bool)
	for _, p := range projs {
		projData, ok := p.(map[string]interface{})
		if !ok {
			continue
		}

		id, _ := projData["id"].(string)
		if id == "" {
			continue
		}

		projIDs[id] = true
		x, _ := projData["x"].(float64)
		y, _ := projData["y"].(float64)
		width, _ := projData["width"].(float64)
		height, _ := projData["height"].(float64)
		element, _ := projData["element"].(string)
		vx, hasVX := projData["vx"].(float64)
		vy, hasVY := projData["vy"].(float64)

		if existing, exists := g.projectiles[id]; exists {
			existing.X = x
			existing.Y = y
			if width > 0 {
				existing.Width = width
			}
			if height > 0 {
				existing.Height = height
			}
			if element != "" {
				existing.Element = element
			}
			if hasVX {
				existing.VX = vx
			}
			if hasVY {
				existing.VY = vy
			}
		} else {
			if width == 0 {
				width = defaultProjectileSize
			}
			if height == 0 {
				height = defaultProjectileSize
			}
			g.projectiles[id] = &Projectile{
				ID:      id,
				X:       x,
				Y:       y,
				VX:      vx,
				VY:      vy,
				Width:   width,
				Height:  height,
				Element: element,
			}
		}
	}

	// サーバーに存在しない投射物を削除
	for id := range g.projectiles {
		if !projIDs[id] {
			delete(g.projectiles, id)
		}
	}
}

// handleCastEvent 魔法詠唱イベントを処理
func (g *Game) handleCastEvent(msg map[string]interface{}, castEvent string) {
	damage, _ := msg["damage"].(float64)
	projectileID, _ := msg["projectileID"].(string)
	x, _ := msg["x"].(float64)
	y, _ := msg["y"].(float64)
	vx, _ := msg["vx"].(float64)
	vy, _ := msg["vy"].(float64)
	width, _ := msg["width"].(float64)
	height, _ := msg["height"].(float64)
	element, _ := msg["element"].(string)

	g.messageMutex.Lock()
	g.lastMessage = fmt.Sprintf("Spell cast: %s (Damage: %.0f)", castEvent, damage)
	g.messageMutex.Unlock()

	if projectileID == "" {
		return
	}

	if width == 0 {
		width = defaultProjectileSize
	}
	if height == 0 {
		height = defaultProjectileSize
	}

	g.projectilesMutex.Lock()
	g.projectiles[projectileID] = &Projectile{
		ID:      projectileID,
		X:       x,
		Y:       y,
		VX:      vx,
		VY:      vy,
		Width:   width,
		Height:  height,
		Element: element,
	}
	g.projectilesMutex.Unlock()
}

func (g *Game) Update() error {
	var dx, dy float64

	if ebiten.IsKeyPressed(ebiten.KeyArrowLeft) {
		dx -= 1
	}
	if ebiten.IsKeyPressed(ebiten.KeyArrowRight) {
		dx += 1
	}
	if ebiten.IsKeyPressed(ebiten.KeyArrowUp) {
		dy -= 1
	}
	if ebiten.IsKeyPressed(ebiten.KeyArrowDown) {
		dy += 1
	}

	if dx != 0 && dy != 0 {
		dx *= 1 / math.Sqrt2
		dy *= 1 / math.Sqrt2
	}

	prevX := g.player.X
	prevY := g.player.Y

	g.player.X += dx * playerSpeed
	g.player.Y += dy * playerSpeed

	if g.player.X < 0 {
		g.player.X = 0
	}
	if g.player.X > screenWidth-playerSize {
		g.player.X = screenWidth - playerSize
	}
	if g.player.Y < territoryBoundary {
		g.player.Y = territoryBoundary
	}
	if g.player.Y > screenHeight-playerSize {
		g.player.Y = screenHeight - playerSize
	}

	inputMoved := g.player.X != prevX || g.player.Y != prevY

	if inputMoved {
		g.connMutex.RLock()
		conn := g.conn
		g.connMutex.RUnlock()

		if conn != nil {
			g.serverPlayerX = g.player.X
			g.serverPlayerY = g.player.Y
			g.hasServerPos = true
			position := map[string]float64{
				"x": g.player.X,
				"y": g.player.Y,
			}
			if err := conn.SendJSON(position); err != nil {
				log.Printf("Write error: %v", err)
			}
		}
	}

	if g.hasServerPos {
		dxTarget := g.serverPlayerX - g.player.X
		dyTarget := g.serverPlayerY - g.player.Y
		correctionDistance := math.Hypot(dxTarget, dyTarget)
		const (
			correctionThreshold = 4.0
			snapThreshold       = 40.0
			correctionFactor    = 0.20
		)
		if correctionDistance > correctionThreshold {
			if correctionDistance > snapThreshold {
				g.player.X = g.serverPlayerX
				g.player.Y = g.serverPlayerY
			} else {
				g.player.X += dxTarget * correctionFactor
				g.player.Y += dyTarget * correctionFactor
			}
			if g.player.X < 0 {
				g.player.X = 0
			}
			if g.player.X > screenWidth-playerSize {
				g.player.X = screenWidth - playerSize
			}
			if g.player.Y < territoryBoundary {
				g.player.Y = territoryBoundary
			}
			if g.player.Y > screenHeight-playerSize {
				g.player.Y = screenHeight - playerSize
			}
		}
	}

	inputChars := ebiten.AppendInputChars(nil)
	for _, char := range inputChars {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') {
			g.typingBuffer += string(char)
		} else if char == ' ' {
			g.typingBuffer += " "
		}
	}

	backspacePressed := ebiten.IsKeyPressed(ebiten.KeyBackspace)
	if backspacePressed && !g.prevBackspace {
		if len(g.typingBuffer) > 0 {
			g.typingBuffer = g.typingBuffer[:len(g.typingBuffer)-1]
		}
	}
	g.prevBackspace = backspacePressed

	spacePressed := ebiten.IsKeyPressed(ebiten.KeySpace)
	if spacePressed && !g.prevSpace {
		g.typingBuffer += " "
	}
	g.prevSpace = spacePressed

	enterPressed := ebiten.IsKeyPressed(ebiten.KeyEnter)
	if enterPressed && !g.prevEnter {
		if len(g.typingBuffer) > 0 {
			g.connMutex.RLock()
			conn := g.conn
			g.connMutex.RUnlock()

			if conn != nil {
				spell := map[string]string{
					"spell": g.typingBuffer,
				}
				if err := conn.SendJSON(spell); err != nil {
					log.Printf("Write error: %v", err)
				}
				g.typingBuffer = ""
			} else {
				g.messageMutex.Lock()
				g.lastMessage = "サーバーに接続されていません"
				g.messageMutex.Unlock()
			}
		}
	}
	g.prevEnter = enterPressed

	return nil
}

// drawDashedLine 破線を描画（横方向のみ対応）
func drawDashedLine(screen *ebiten.Image, x1, y1, x2, y2, lineWidth, dashLength, gapLength float32, clr color.Color) {
	// 横方向の線のみをサポート（y1 == y2）
	if y1 != y2 {
		// 縦方向の線の場合は通常の線として描画
		vector.DrawFilledRect(screen, x1-lineWidth/2, y1, lineWidth, y2-y1, clr, false)
		return
	}

	// 横方向の破線を描画
	startX := x1
	if x2 < x1 {
		startX = x2
	}
	length := float32(math.Abs(float64(x2 - x1)))

	currentX := float32(0)
	drawing := true

	for currentX < length {
		segmentLength := dashLength
		if !drawing {
			segmentLength = gapLength
		}

		if currentX+segmentLength > length {
			segmentLength = length - currentX
		}

		if drawing {
			// 線分を描画
			vector.DrawFilledRect(screen, startX+currentX, y1-lineWidth/2, segmentLength, lineWidth, clr, false)
		}

		currentX += segmentLength
		drawing = !drawing
	}
}

func elementColor(element string) color.Color {
	switch strings.ToLower(element) {
	case "fire":
		return color.RGBA{255, 90, 0, 255}
	case "water":
		return color.RGBA{64, 164, 223, 255}
	case "ice":
		return color.RGBA{180, 220, 255, 255}
	case "lightning":
		return color.RGBA{255, 255, 120, 255}
	case "earth":
		return color.RGBA{184, 134, 11, 255}
	case "wind":
		return color.RGBA{144, 238, 144, 255}
	case "arcane":
		return color.RGBA{186, 85, 211, 255}
	default:
		return color.RGBA{255, 165, 0, 255}
	}
}

func (g *Game) Draw(screen *ebiten.Image) {
	screen.Fill(color.RGBA{0, 0, 0, 255})

	const (
		boundaryY  = float32(territoryBoundary)
		lineWidth  = float32(2.0)
		dashLength = float32(10.0)
		gapLength  = float32(5.0)
	)

	drawDashedLine(screen, 0, boundaryY, float32(screenWidth), boundaryY, lineWidth, dashLength, gapLength, color.White)

	// 自分の陣地（下側）の境界線
	vector.DrawFilledRect(screen, 0, boundaryY, lineWidth, float32(screenHeight-territoryBoundary), color.White, false)
	vector.DrawFilledRect(screen, float32(screenWidth-lineWidth), boundaryY, lineWidth, float32(screenHeight-territoryBoundary), color.White, false)
	vector.DrawFilledRect(screen, 0, float32(screenHeight-lineWidth), float32(screenWidth), lineWidth, color.White, false)

	// 相手陣地（上側）の境界線
	vector.DrawFilledRect(screen, 0, 0, lineWidth, boundaryY, color.White, false)
	vector.DrawFilledRect(screen, float32(screenWidth-lineWidth), 0, lineWidth, boundaryY, color.White, false)
	vector.DrawFilledRect(screen, 0, 0, float32(screenWidth), lineWidth, color.White, false)

	g.playersMutex.RLock()
	for _, otherPlayer := range g.otherPlayers {
		width := float32(playerSize)
		height := float32(playerSize)
		vector.DrawFilledRect(screen, float32(otherPlayer.X), float32(otherPlayer.Y), width, height, color.RGBA{255, 0, 0, 255}, false)
	}
	g.playersMutex.RUnlock()

	g.projectilesMutex.RLock()
	activeProjectiles := make([]*Projectile, 0, len(g.projectiles))
	for _, proj := range g.projectiles {
		width := proj.Width
		height := proj.Height
		if width == 0 {
			width = defaultProjectileSize
		}
		if height == 0 {
			height = defaultProjectileSize
		}
		proj.X += proj.VX
		proj.Y += proj.VY
		projColor := elementColor(proj.Element)
		vector.DrawFilledRect(screen, float32(proj.X), float32(proj.Y), float32(width), float32(height), projColor, false)
		if proj.X >= -width && proj.X <= float64(screenWidth) && proj.Y >= -height && proj.Y <= float64(screenHeight) {
			activeProjectiles = append(activeProjectiles, proj)
		}
	}
	g.projectilesMutex.RUnlock()
	g.projectilesMutex.Lock()
	g.projectiles = make(map[string]*Projectile, len(activeProjectiles))
	for _, proj := range activeProjectiles {
		g.projectiles[proj.ID] = proj
	}
	g.projectilesMutex.Unlock()

	vector.DrawFilledRect(screen, float32(g.player.X), float32(g.player.Y), float32(playerSize), float32(playerSize), color.White, false)

	hpBar := fmt.Sprintf("HP: %d/%d", g.player.HP, maxHP)
	mpBar := fmt.Sprintf("MP: %d/%d", g.player.MP, maxMP)
	ebitenutil.DebugPrintAt(screen, hpBar, 10, screenHeight-60)
	ebitenutil.DebugPrintAt(screen, mpBar, 10, screenHeight-45)

	yOffset := 10
	g.playersMutex.RLock()
	for _, otherPlayer := range g.otherPlayers {
		otherHpBar := fmt.Sprintf("Enemy HP: %d/%d", otherPlayer.HP, maxHP)
		otherMpBar := fmt.Sprintf("Enemy MP: %d/%d", otherPlayer.MP, maxMP)
		ebitenutil.DebugPrintAt(screen, otherHpBar, screenWidth-200, yOffset)
		ebitenutil.DebugPrintAt(screen, otherMpBar, screenWidth-200, yOffset+15)
		yOffset += 30
	}
	g.playersMutex.RUnlock()

	if len(g.typingBuffer) > 0 {
		ebitenutil.DebugPrintAt(screen, g.typingBuffer, 10, screenHeight-30)
	}

	g.messageMutex.RLock()
	if len(g.lastMessage) > 0 {
		ebitenutil.DebugPrintAt(screen, g.lastMessage, 10, screenHeight-75)
	}
	g.messageMutex.RUnlock()

	g.connMutex.RLock()
	if g.connectionError != "" {
		ebitenutil.DebugPrintAt(screen, g.connectionError, 10, 10)
	}
	g.connMutex.RUnlock()
}

func (g *Game) Layout(outsideWidth, outsideHeight int) (int, int) {
	return screenWidth, screenHeight
}

func main() {
	ebiten.SetWindowSize(screenWidth, screenHeight)
	ebiten.SetWindowTitle("Typing Magic")

	if err := ebiten.RunGame(NewGame()); err != nil {
		panic(err)
	}
}
