package main

import (
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		origin := r.Header.Get("Origin")

		// 開発環境（localhost）は常に許可
		if origin == "http://localhost:3000" || origin == "http://localhost:8080" ||
			origin == "http://127.0.0.1:3000" || origin == "http://127.0.0.1:8080" ||
			origin == "" {
			return true
		}

		// CORS_ORIGIN環境変数が設定されている場合は、そのオリジンのみ許可
		corsOrigin := os.Getenv("CORS_ORIGIN")
		if corsOrigin != "" {
			return origin == corsOrigin
		}

		// 開発環境では全てのオリジンを許可
		return true
	},
}

const (
	playerSize        = 10 // クライアント側と同じサイズ
	screenWidth       = 480
	screenHeight      = 640
	territoryBoundary = screenHeight / 2 // 陣地の境界線（画面の中央）
)

// プレイヤーの状態
type PlayerState struct {
	HP       int
	MP       int
	X        float64
	Y        float64
	IsBottom bool // 下側の陣地にいるかどうか（true: 下側、false: 上側）
}

// 投射物生成関数の型
// 引数: casterX, casterY（発射者の座標）, isBottom（下側の陣地かどうか）, casterID
// 戻り値: 生成された投射物のスライス
type ProjectileSpawner func(casterX, casterY float64, isBottom bool, casterID string) []*Projectile

// 魔法の定義
type Magic struct {
	Name             string  // 魔法名
	Cost             int     // MP消費量
	Damage           int     // ダメージ
	Speed            float64 // 投射物の速度
	Width            float64 // 投射物の横幅
	Height           float64 // 投射物の縦幅
	Lifetime         int     // 投射物の寿命（フレーム数）
	Element          string
	SpawnType        string
	SpawnProjectiles ProjectileSpawner // 投射物生成関数
}

func (m *Magic) configureSpawner() {
	switch m.SpawnType {
	case "single":
		m.SpawnProjectiles = func(casterX, casterY float64, isBottom bool, casterID string) []*Projectile {
			return m.spawnSingleProjectile(casterX, casterY, isBottom, casterID)
		}
	case "wide":
		m.SpawnProjectiles = func(casterX, casterY float64, isBottom bool, casterID string) []*Projectile {
			return m.spawnWideProjectile(casterX, casterY, isBottom, casterID)
		}
	case "multi":
		m.SpawnProjectiles = func(casterX, casterY float64, isBottom bool, casterID string) []*Projectile {
			return m.spawnMultiProjectile(casterX, casterY, isBottom, casterID)
		}
	case "stream":
		m.SpawnProjectiles = func(casterX, casterY float64, isBottom bool, casterID string) []*Projectile {
			return m.spawnStreamProjectiles(casterX, casterY, isBottom, casterID)
		}
	case "blizzard":
		m.SpawnProjectiles = func(casterX, casterY float64, isBottom bool, casterID string) []*Projectile {
			return m.spawnBlizzardProjectiles(casterX, casterY, isBottom, casterID)
		}
	case "gale":
		m.SpawnProjectiles = func(casterX, casterY float64, isBottom bool, casterID string) []*Projectile {
			return m.spawnGaleProjectiles(casterX, casterY, isBottom, casterID)
		}
	default:
		m.SpawnProjectiles = func(casterX, casterY float64, isBottom bool, casterID string) []*Projectile {
			return m.spawnSingleProjectile(casterX, casterY, isBottom, casterID)
		}
	}
}

// 投射物生成関数の実装

// 通常の単一投射物を生成（fireball用）
func (m Magic) spawnSingleProjectile(casterX, casterY float64, isBottom bool, casterID string) []*Projectile {
	projectileIDMutex.Lock()
	projectileIDCounter++
	projID := fmt.Sprintf("proj_%d", projectileIDCounter)
	projectileIDMutex.Unlock()

	var vY float64
	var startY float64
	if isBottom {
		vY = -m.Speed
		startY = casterY - playerSize
	} else {
		vY = m.Speed
		startY = casterY + playerSize
	}

	return []*Projectile{
		{
			ID:       projID,
			X:        casterX + playerSize/2,
			Y:        startY,
			VX:       0,
			VY:       vY,
			Width:    m.Width,
			Height:   m.Height,
			Damage:   m.Damage,
			CasterID: casterID,
			Lifetime: m.Lifetime,
			Element:  m.Element,
		},
	}
}

// 横幅が大きいが威力が低い投射物を生成（wideball用）
func (m Magic) spawnWideProjectile(casterX, casterY float64, isBottom bool, casterID string) []*Projectile {
	projectileIDMutex.Lock()
	projectileIDCounter++
	projID := fmt.Sprintf("proj_%d", projectileIDCounter)
	projectileIDMutex.Unlock()

	var vY float64
	var startY float64
	if isBottom {
		vY = -m.Speed
		startY = casterY - playerSize
	} else {
		vY = m.Speed
		startY = casterY + playerSize
	}

	playerCenterX := casterX + playerSize/2

	return []*Projectile{
		{
			ID:       projID,
			X:        playerCenterX - m.Width/2, // 投射物の中心をプレイヤーの中心に合わせる
			Y:        startY,
			VX:       0,
			VY:       vY,
			Width:    m.Width,
			Height:   m.Height,
			Damage:   m.Damage,
			CasterID: casterID,
			Lifetime: m.Lifetime,
			Element:  m.Element,
		},
	}
}

// 複数方向に投射物を発射（multi用）
func (m Magic) spawnMultiProjectile(casterX, casterY float64, isBottom bool, casterID string) []*Projectile {
	var baseVY float64
	var startY float64
	if isBottom {
		baseVY = -m.Speed
		startY = casterY - playerSize
	} else {
		baseVY = m.Speed
		startY = casterY + playerSize
	}

	// 3方向に発射（中央、左、右）
	projectiles := make([]*Projectile, 0, 3)
	spreadAngle := 0.3

	projectileIDMutex.Lock()
	for i := 0; i < 3; i++ {
		projectileIDCounter++
		projID := fmt.Sprintf("proj_%d", projectileIDCounter)

		var vX, vY float64
		switch i {
		case 0:
			vX = 0
			vY = baseVY
		case 1:
			vX = -m.Speed * math.Sin(spreadAngle)
			vY = baseVY * math.Cos(spreadAngle)
		default:
			vX = m.Speed * math.Sin(spreadAngle)
			vY = baseVY * math.Cos(spreadAngle)
		}

		projectiles = append(projectiles, &Projectile{
			ID:       projID,
			X:        casterX + playerSize/2,
			Y:        startY,
			VX:       vX,
			VY:       vY,
			Width:    m.Width,
			Height:   m.Height,
			Damage:   m.Damage,
			CasterID: casterID,
			Lifetime: m.Lifetime,
			Element:  m.Element,
		})
	}
	projectileIDMutex.Unlock()

	return projectiles
}

// 水流のように横幅のある複数の投射物を生成
func (m Magic) spawnStreamProjectiles(casterX, casterY float64, isBottom bool, casterID string) []*Projectile {
	var vY float64
	var startY float64
	if isBottom {
		vY = -m.Speed
		startY = casterY - playerSize
	} else {
		vY = m.Speed
		startY = casterY + playerSize
	}

	offsets := []float64{-m.Width, 0, m.Width}
	projectiles := make([]*Projectile, 0, len(offsets))

	projectileIDMutex.Lock()
	for _, offset := range offsets {
		projectileIDCounter++
		projID := fmt.Sprintf("proj_%d", projectileIDCounter)

		startX := casterX + playerSize/2 + offset/2
		vx := offset * 0.05

		projectiles = append(projectiles, &Projectile{
			ID:       projID,
			X:        startX,
			Y:        startY,
			VX:       vx,
			VY:       vY,
			Width:    m.Width,
			Height:   m.Height,
			Damage:   m.Damage,
			CasterID: casterID,
			Lifetime: m.Lifetime,
			Element:  m.Element,
		})
	}
	projectileIDMutex.Unlock()

	return projectiles
}

// 氷の粒が広がるように散布する投射物を生成
func (m Magic) spawnBlizzardProjectiles(casterX, casterY float64, isBottom bool, casterID string) []*Projectile {
	var baseVY float64
	var startY float64
	if isBottom {
		baseVY = -m.Speed
		startY = casterY - playerSize
	} else {
		baseVY = m.Speed
		startY = casterY + playerSize
	}

	velocities := []float64{-1.5, -0.75, 0, 0.75, 1.5}
	projectiles := make([]*Projectile, 0, len(velocities))

	projectileIDMutex.Lock()
	for _, vx := range velocities {
		projectileIDCounter++
		projID := fmt.Sprintf("proj_%d", projectileIDCounter)

		projectiles = append(projectiles, &Projectile{
			ID:       projID,
			X:        casterX + playerSize/2,
			Y:        startY,
			VX:       vx,
			VY:       baseVY,
			Width:    m.Width,
			Height:   m.Height,
			Damage:   m.Damage,
			CasterID: casterID,
			Lifetime: m.Lifetime,
			Element:  m.Element,
		})
	}
	projectileIDMutex.Unlock()

	return projectiles
}

// 風の刃のように左右へ広がる投射物を生成
func (m Magic) spawnGaleProjectiles(casterX, casterY float64, isBottom bool, casterID string) []*Projectile {
	var baseVY float64
	var startY float64
	if isBottom {
		baseVY = -m.Speed
		startY = casterY - playerSize
	} else {
		baseVY = m.Speed
		startY = casterY + playerSize
	}

	velocities := []float64{-3.0, -1.5, 1.5, 3.0}
	projectiles := make([]*Projectile, 0, len(velocities))

	projectileIDMutex.Lock()
	for _, vx := range velocities {
		projectileIDCounter++
		projID := fmt.Sprintf("proj_%d", projectileIDCounter)

		projectiles = append(projectiles, &Projectile{
			ID:       projID,
			X:        casterX + playerSize/2,
			Y:        startY,
			VX:       vx,
			VY:       baseVY * 0.9,
			Width:    m.Width,
			Height:   m.Height,
			Damage:   m.Damage,
			CasterID: casterID,
			Lifetime: m.Lifetime,
			Element:  m.Element,
		})
	}
	projectileIDMutex.Unlock()

	return projectiles
}

// 魔法の定義マップ
var magicDefs = map[string]Magic{
	"fire": func() Magic {
		m := Magic{
			Name:      "fire",
			Cost:      10,
			Damage:    12,
			Speed:     5.0,
			Width:     5.0,
			Height:    5.0,
			Lifetime:  300,
			Element:   "fire",
			SpawnType: "single",
		}
		m.configureSpawner()
		return m
	}(),
	"flare": func() Magic {
		m := Magic{
			Name:      "flare",
			Cost:      35,
			Damage:    45,
			Speed:     4.2,
			Width:     12.0,
			Height:    12.0,
			Lifetime:  320,
			Element:   "fire",
			SpawnType: "single",
		}
		m.configureSpawner()
		return m
	}(),
	"aqua": func() Magic {
		m := Magic{
			Name:      "aqua",
			Cost:      20,
			Damage:    18,
			Speed:     3.5,
			Width:     10.0,
			Height:    10.0,
			Lifetime:  320,
			Element:   "water",
			SpawnType: "stream",
		}
		m.configureSpawner()
		return m
	}(),
	"gaiawave": func() Magic {
		m := Magic{
			Name:      "gaiawave",
			Cost:      25,
			Damage:    18,
			Speed:     3.8,
			Width:     32.0,
			Height:    12.0,
			Lifetime:  320,
			Element:   "earth",
			SpawnType: "wide",
		}
		m.configureSpawner()
		return m
	}(),
	"arcstorm": func() Magic {
		m := Magic{
			Name:      "arcstorm",
			Cost:      30,
			Damage:    20,
			Speed:     5.2,
			Width:     5.0,
			Height:    5.0,
			Lifetime:  300,
			Element:   "arcane",
			SpawnType: "multi",
		}
		m.configureSpawner()
		return m
	}(),
	"blizzard": func() Magic {
		m := Magic{
			Name:      "blizzard",
			Cost:      22,
			Damage:    12,
			Speed:     3.2,
			Width:     7.0,
			Height:    7.0,
			Lifetime:  320,
			Element:   "ice",
			SpawnType: "blizzard",
		}
		m.configureSpawner()
		return m
	}(),
	"thunder": func() Magic {
		m := Magic{
			Name:      "thunder",
			Cost:      28,
			Damage:    35,
			Speed:     8.0,
			Width:     4.0,
			Height:    16.0,
			Lifetime:  280,
			Element:   "lightning",
			SpawnType: "single",
		}
		m.configureSpawner()
		return m
	}(),
	"stone": func() Magic {
		m := Magic{
			Name:      "stone",
			Cost:      30,
			Damage:    32,
			Speed:     3.0,
			Width:     18.0,
			Height:    18.0,
			Lifetime:  350,
			Element:   "earth",
			SpawnType: "single",
		}
		m.configureSpawner()
		return m
	}(),
	"gale": func() Magic {
		m := Magic{
			Name:      "gale",
			Cost:      24,
			Damage:    14,
			Speed:     6.0,
			Width:     6.0,
			Height:    10.0,
			Lifetime:  280,
			Element:   "wind",
			SpawnType: "gale",
		}
		m.configureSpawner()
		return m
	}(),
}

// 魔法投射物
type Projectile struct {
	ID       string
	X        float64
	Y        float64
	VX       float64
	VY       float64
	Width    float64
	Height   float64
	Damage   int
	CasterID string
	Lifetime int
	Element  string
}

// 魔法投射物の管理
var projectiles = make(map[string]*Projectile)
var projectilesMutex sync.RWMutex
var projectileIDCounter int
var projectileIDMutex sync.Mutex

type SpellModifier struct {
	DamageMul float64
	SizeMul   float64
	CostMul   float64
}

var prefixModifiers = map[string]SpellModifier{
	"grand": {DamageMul: 1.3, SizeMul: 1.2, CostMul: 1.2},
	"mega":  {DamageMul: 1.5, SizeMul: 1.35, CostMul: 1.35},
	"swift": {DamageMul: 1.1, SizeMul: 1.0, CostMul: 1.05},
}

var suffixModifiers = map[string]SpellModifier{
	"burst": {DamageMul: 1.25, SizeMul: 1.1, CostMul: 1.1},
	"surge": {DamageMul: 1.2, SizeMul: 1.15, CostMul: 1.15},
	"nova":  {DamageMul: 1.35, SizeMul: 1.25, CostMul: 1.3},
}

// 接続されているクライアントを管理
var clients = make(map[*websocket.Conn]bool)
var clientsMutex sync.RWMutex

// 各接続の書き込み用Mutex（WebSocketへの同時書き込みを防ぐ）
var connWriteMutexes = make(map[*websocket.Conn]*sync.Mutex)
var connWriteMutexesMutex sync.Mutex

// プレイヤーの状態を管理
var playerStates = make(map[*websocket.Conn]*PlayerState)
var playerStatesMutex sync.RWMutex

func handleConnections(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("WebSocket upgrade error: %v", err)
		return
	}
	defer conn.Close()

	// クライアントを管理マップに追加
	clientsMutex.Lock()
	clients[conn] = true
	clientsMutex.Unlock()

	// 接続の書き込み用Mutexを追加
	connWriteMutexesMutex.Lock()
	connWriteMutexes[conn] = &sync.Mutex{}
	connWriteMutexesMutex.Unlock()

	// プレイヤーの状態を初期化
	// 接続順にプレイヤーを配置（最初のプレイヤーは下側、2番目は上側）
	playerStatesMutex.Lock()
	playerCount := len(playerStates)
	var startY float64
	var isBottom bool
	switch playerCount {
	case 0:
		// 最初のプレイヤーは下側の陣地（画面下部）
		startY = territoryBoundary + 50
		isBottom = true
	case 1:
		// 2番目以降のプレイヤーは上側の陣地（画面上部）
		startY = territoryBoundary - 50
		isBottom = false
	default:
		log.Printf("over player count error: %d", playerCount)
		return
	}
	playerStates[conn] = &PlayerState{
		HP:       100,
		MP:       100,
		X:        screenWidth / 2, // 画面中央
		Y:        startY,
		IsBottom: isBottom,
	}
	playerStatesMutex.Unlock()

	// クライアントに自分のIDと陣地情報を送信
	myID := conn.RemoteAddr().String()
	initMsg := map[string]interface{}{
		"type":     "init",
		"myID":     myID,
		"isBottom": isBottom,
	}
	initJSON, _ := json.Marshal(initMsg)
	if err := safeWriteMessage(conn, websocket.TextMessage, initJSON); err != nil {
		log.Printf("Failed to send init message: %v", err)
	}

	// プレイヤー情報を全クライアントに送信
	broadcastPlayerStates()

	log.Println("Client connected")

	// クライアントが切断したら、管理マップから削除
	defer func() {
		clientsMutex.Lock()
		delete(clients, conn)
		clientsMutex.Unlock()

		playerStatesMutex.Lock()
		delete(playerStates, conn)
		playerStatesMutex.Unlock()

		connWriteMutexesMutex.Lock()
		delete(connWriteMutexes, conn)
		connWriteMutexesMutex.Unlock()

		log.Println("Client disconnected")
	}()

	// メッセージを受信して処理
	for {
		messageType, message, err := conn.ReadMessage()
		if err != nil {
			log.Printf("Read error: %v", err)
			break
		}

		// JSONメッセージを解析
		var msg map[string]interface{}
		if err := json.Unmarshal(message, &msg); err != nil {
			// JSONではない場合は、従来通りブロードキャスト
			broadcastMessage(conn, messageType, message)
			continue
		}

		// 魔法の送信を処理
		if spellName, ok := msg["spell"].(string); ok {
			handleSpellCast(conn, spellName)
			continue
		}

		// 位置情報の処理
		if x, ok := msg["x"].(float64); ok {
			if y, ok := msg["y"].(float64); ok {
				// プレイヤーの位置を更新
				playerStatesMutex.Lock()
				var correctedX, correctedY float64
				var positionCorrected bool
				if state, exists := playerStates[conn]; exists {
					correctedX = x
					// クライアントから送られてくるY座標は、クライアントの視点での座標
					// サーバー側の物理座標に変換する必要がある
					correctedY = y
					if !state.IsBottom {
						// 上側のプレイヤーの場合、クライアントは下側の視点で座標を送ってくる
						// サーバー側の物理座標に変換（反転）
						correctedY = screenHeight - y
					}

					// 陣地制限チェック（IsBottomフラグに基づいて判定）
					if state.IsBottom {
						// 下側の陣地のプレイヤーは上側に出られない
						if correctedY < territoryBoundary {
							correctedY = territoryBoundary
							positionCorrected = true
						}
					} else {
						// 上側の陣地のプレイヤーは下側に出られない
						if correctedY >= territoryBoundary {
							correctedY = territoryBoundary - 1
							positionCorrected = true
						}
					}

					// 画面外に出ないように制限
					if correctedX < 0 {
						correctedX = 0
						positionCorrected = true
					}
					if correctedX > screenWidth-playerSize {
						correctedX = screenWidth - playerSize
						positionCorrected = true
					}
					if state.IsBottom {
						// 下側の陣地のプレイヤーは下側の範囲内のみ
						if correctedY < territoryBoundary {
							correctedY = territoryBoundary
							positionCorrected = true
						}
						if correctedY > screenHeight-playerSize {
							correctedY = screenHeight - playerSize
							positionCorrected = true
						}
					} else {
						// 上側の陣地のプレイヤーは上側の範囲内のみ
						if correctedY < 0 {
							correctedY = 0
							positionCorrected = true
						}
						if correctedY >= territoryBoundary {
							correctedY = territoryBoundary - 1
							positionCorrected = true
						}
					}

					state.X = correctedX
					state.Y = correctedY
				}
				playerStatesMutex.Unlock()

				// 位置が修正された場合、クライアントに修正後の位置を送信
				// クライアントの視点に応じて座標を変換する必要がある
				if positionCorrected {
					playerStatesMutex.RLock()
					var displayY float64 = correctedY
					if state, exists := playerStates[conn]; exists {
						// クライアントの視点に応じて座標を変換
						if state.IsBottom {
							// 下側のプレイヤーの場合、座標はそのまま
							displayY = correctedY
						} else {
							// 上側のプレイヤーの場合、座標を反転（自分の視点では下側にいるため）
							displayY = screenHeight - correctedY
						}
					}
					playerStatesMutex.RUnlock()

					correctedPosition := map[string]float64{
						"x": correctedX,
						"y": displayY,
					}
					correctedJSON, _ := json.Marshal(correctedPosition)
					if err := safeWriteMessage(conn, websocket.TextMessage, correctedJSON); err != nil {
						log.Printf("Failed to send corrected position: %v", err)
					}
				}

				// プレイヤー状態を全クライアントに送信
				broadcastPlayerStates()
			}
		} else {
			// その他のメッセージは従来通りブロードキャスト
			broadcastMessage(conn, messageType, message)
		}
	}
}

// 安全にWebSocketに書き込むヘルパー関数
func safeWriteMessage(conn *websocket.Conn, messageType int, data []byte) error {
	connWriteMutexesMutex.Lock()
	mu, exists := connWriteMutexes[conn]
	connWriteMutexesMutex.Unlock()
	if !exists {
		return fmt.Errorf("connection not found")
	}
	mu.Lock()
	defer mu.Unlock()
	return conn.WriteMessage(messageType, data)
}

// メッセージをブロードキャスト
func broadcastMessage(sender *websocket.Conn, messageType int, message []byte) {
	clientsMutex.RLock()
	deadClients := make([]*websocket.Conn, 0)
	for client := range clients {
		if sender == nil || client != sender { // senderがnilの場合は全員に送信
			if err := safeWriteMessage(client, messageType, message); err != nil {
				log.Printf("Write error: %v", err)
				client.Close()
				deadClients = append(deadClients, client)
			}
		}
	}
	clientsMutex.RUnlock()

	// エラーが発生したクライアントを管理マップから削除
	if len(deadClients) > 0 {
		clientsMutex.Lock()
		for _, client := range deadClients {
			delete(clients, client)
		}
		clientsMutex.Unlock()

		playerStatesMutex.Lock()
		for _, client := range deadClients {
			delete(playerStates, client)
		}
		playerStatesMutex.Unlock()
	}
}

// 魔法の詠唱を処理
func handleSpellCast(conn *websocket.Conn, spellName string) {
	// 魔法名を小文字に変換して統一
	spellName = strings.ToLower(spellName)
	words := strings.Fields(spellName)
	if len(words) == 0 {
		return
	}

	damageMul := 1.0
	sizeMul := 1.0
	costMul := 1.0

	if mod, ok := prefixModifiers[words[0]]; ok {
		damageMul *= mod.DamageMul
		sizeMul *= mod.SizeMul
		costMul *= mod.CostMul
		words = words[1:]
	}

	if len(words) == 0 {
		return
	}

	if mod, ok := suffixModifiers[words[len(words)-1]]; ok {
		damageMul *= mod.DamageMul
		sizeMul *= mod.SizeMul
		costMul *= mod.CostMul
		words = words[:len(words)-1]
	}

	if len(words) == 0 {
		return
	}

	baseSpell := strings.Join(words, " ")

	// プレイヤーの状態を取得
	playerStatesMutex.RLock()
	playerState, exists := playerStates[conn]
	playerStatesMutex.RUnlock()

	if !exists {
		log.Printf("Player state not found for connection")
		return
	}

	// 魔法の判定
	baseMagic, found := magicDefs[baseSpell]
	if !found {
		log.Printf("Unknown spell: %s", spellName)
		return
	}

	magic := baseMagic
	magic.Cost = int(math.Round(float64(magic.Cost) * costMul))
	if magic.Cost < 1 {
		magic.Cost = 1
	}
	magic.Damage = int(math.Round(float64(magic.Damage) * damageMul))
	if magic.Damage < 1 {
		magic.Damage = 1
	}
	magic.Width *= sizeMul
	magic.Height *= sizeMul
	magic.configureSpawner()

	// MPが足りるかチェック
	if playerState.MP < magic.Cost {
		log.Printf("Insufficient MP: %d < %d", playerState.MP, magic.Cost)
		return
	}

	// MPを消費
	playerStatesMutex.Lock()
	playerState.MP -= magic.Cost

	casterX := playerState.X
	casterY := playerState.Y
	casterID := conn.RemoteAddr().String()
	playerStatesMutex.Unlock()

	log.Printf("Spell cast: %s (actual: %s) by player, Cost: %d, Remaining MP: %d",
		spellName, baseSpell, magic.Cost, playerState.MP)

	// 投射物生成関数を使って投射物を生成
	spawnedProjectiles := magic.SpawnProjectiles(casterX, casterY, playerState.IsBottom, casterID)

	// 生成された投射物を管理マップに追加
	projectilesMutex.Lock()
	for _, proj := range spawnedProjectiles {
		projectiles[proj.ID] = proj
	}
	projectilesMutex.Unlock()

	// 全クライアントに魔法発射イベントをブロードキャスト（各クライアントの視点に応じて反転）
	clientsMutex.RLock()
	allClients := make([]*websocket.Conn, 0, len(clients))
	for client := range clients {
		allClients = append(allClients, client)
	}
	clientsMutex.RUnlock()

	// 各投射物ごとにイベントを送信
	for _, projectile := range spawnedProjectiles {
		for _, targetConn := range allClients {
			// 投射物の位置を各クライアントの視点に応じて反転
			displayX := projectile.X
			var displayY float64
			displayVX := projectile.VX
			var displayVY float64

			// ターゲットクライアントの陣地情報を取得
			var targetIsBottom bool
			playerStatesMutex.RLock()
			if targetState, exists := playerStates[targetConn]; exists {
				targetIsBottom = targetState.IsBottom
			}
			playerStatesMutex.RUnlock()

			// ターゲットの視点に応じて投射物の座標と速度を変換
			targetID := targetConn.RemoteAddr().String()
			if targetID == casterID {
				// 自分が発射した投射物
				if targetIsBottom {
					// ターゲットが下側にいる場合、自分の座標はそのまま
					displayY = projectile.Y
					displayVY = projectile.VY
				} else {
					// ターゲットが上側にいる場合、自分の座標を反転（自分の視点では下側にいるため）
					displayY = screenHeight - projectile.Y
					displayVX = -projectile.VX // 横方向の速度も反転
					displayVY = -projectile.VY // 縦方向の速度も反転
				}
			} else {
				// 相手が発射した投射物
				if targetIsBottom {
					// ターゲットが下側にいる場合、相手は上側（物理座標）にいる
					// 物理座標をそのまま送る（反転しない）
					displayY = projectile.Y
					displayVY = projectile.VY
				} else {
					// ターゲットが上側にいる場合、相手は下側（物理座標）にいる
					// 物理座標を反転して送る
					displayY = screenHeight - projectile.Y
					displayVX = -projectile.VX // 横方向の速度も反転
					displayVY = -projectile.VY // 縦方向の速度も反転
				}
			}

			castEvent := map[string]interface{}{
				"castEvent":    spellName,
				"casterID":     casterID,
				"damage":       projectile.Damage,
				"projectileID": projectile.ID,
				"x":            displayX,
				"y":            displayY,
				"vx":           displayVX,
				"vy":           displayVY,
				"width":        projectile.Width,
				"height":       projectile.Height,
				"element":      projectile.Element,
			}

			eventJSON, err := json.Marshal(castEvent)
			if err != nil {
				log.Printf("Failed to marshal cast event: %v", err)
				continue
			}

			if err := safeWriteMessage(targetConn, websocket.TextMessage, eventJSON); err != nil {
				log.Printf("Failed to send cast event: %v", err)
			}
		}
	}

	// プレイヤー状態を更新
	broadcastPlayerStates()
}

// 全プレイヤーの状態をブロードキャスト
func broadcastPlayerStates() {
	playerStatesMutex.RLock()
	// 各クライアントに対して、自分の位置と相手の位置を反転させて送信
	clientsMutex.RLock()
	allClients := make([]*websocket.Conn, 0, len(clients))
	for client := range clients {
		allClients = append(allClients, client)
	}
	clientsMutex.RUnlock()

	for _, targetConn := range allClients {
		targetID := targetConn.RemoteAddr().String()
		states := make(map[string]map[string]interface{})

		// 各プレイヤーの状態を、画面反転を考慮して送信
		// ターゲットクライアントの陣地情報を取得
		var targetIsBottom bool
		if targetState, exists := playerStates[targetConn]; exists {
			targetIsBottom = targetState.IsBottom
		}

		for conn, state := range playerStates {
			playerID := conn.RemoteAddr().String()
			var displayY float64

			// 各プレイヤーの座標を、ターゲットクライアントの視点に応じて変換
			// 基本的な考え方：
			// - ターゲット自身の座標は、ターゲットの視点に合わせて変換（上側のプレイヤーは反転）
			// - 相手の座標は、ターゲットの視点に合わせて変換（ターゲットとは反対側にいるプレイヤーは反転）
			if playerID == targetID {
				// 自分の座標
				if targetIsBottom {
					// ターゲットが下側にいる場合、自分の座標はそのまま
					displayY = state.Y
				} else {
					// ターゲットが上側にいる場合、自分の座標を反転（自分の視点では下側にいるため）
					displayY = screenHeight - state.Y
				}
			} else {
				// 相手の座標
				// 基本的な考え方：相手は常にターゲットとは反対側にいる
				// - ターゲットが下側の場合、相手は上側（物理座標）にいる
				// - ターゲットが上側の場合、相手は下側（物理座標）にいる
				// 相手の座標をターゲットの視点に合わせて変換する必要がある
				if targetIsBottom {
					// ターゲットが下側にいる場合、相手は上側（物理座標）にいる
					// プレイヤー1から見ると、プレイヤー2は上側（画面の上部）に表示されるべき
					// 物理座標Y=270（上側）をそのまま送る（反転しない）
					displayY = state.Y
				} else {
					// ターゲットが上側にいる場合、相手は下側（物理座標）にいる
					// プレイヤー2から見ると、プレイヤー1は上側（画面の上部）に表示されるべき
					// 物理座標Y=370（下側）を反転して送る（Y=640-370=270）
					displayY = screenHeight - state.Y
				}
			}

			states[playerID] = map[string]interface{}{
				"hp": state.HP,
				"mp": state.MP,
				"x":  state.X,
				"y":  displayY,
			}
		}

		updateMsg := map[string]interface{}{
			"type":   "playerStates",
			"states": states,
		}

		msgJSON, err := json.Marshal(updateMsg)
		if err != nil {
			log.Printf("Failed to marshal player states: %v", err)
			continue
		}
		if err := safeWriteMessage(targetConn, websocket.TextMessage, msgJSON); err != nil {
			log.Printf("Failed to send player states to client %s: %v", targetID, err)
			// エラーが発生したクライアントは後で削除される
		}
	}

	playerStatesMutex.RUnlock()
}

// ゲームループ（魔法投射物の更新と当たり判定、MP回復）
func gameLoop() {
	ticker := time.NewTicker(16 * time.Millisecond) // 約60FPS
	defer ticker.Stop()

	mpRegenTicker := time.NewTicker(1 * time.Second) // MP回復は1秒ごと
	defer mpRegenTicker.Stop()

	for {
		select {
		case <-ticker.C:
			updateProjectiles()
		case <-mpRegenTicker.C:
			regenerateMP()
		}
	}
}

// 投射物の更新と当たり判定
func updateProjectiles() {
	// 投射物の更新
	projectilesMutex.Lock()
	deadProjectiles := make([]string, 0)
	hasHit := false
	for id, proj := range projectiles {
		// 位置を更新
		proj.X += proj.VX
		proj.Y += proj.VY
		proj.Lifetime--

		// 画面外または寿命切れで削除
		if proj.X < -10 || proj.X > float64(screenWidth+10) || proj.Y < -10 || proj.Y > float64(screenHeight+10) || proj.Lifetime <= 0 {
			deadProjectiles = append(deadProjectiles, id)
			continue
		}

		// プレイヤーとの当たり判定
		playerStatesMutex.RLock()
		for conn, state := range playerStates {
			// 発射者には当たらない
			if conn.RemoteAddr().String() == proj.CasterID {
				continue
			}

			// 当たり判定（簡易的な矩形判定）
			// 投射物の中心とプレイヤーの中心の距離を計算
			projCenterX := proj.X + proj.Width/2
			projCenterY := proj.Y + proj.Height/2
			playerCenterX := state.X + playerSize/2
			playerCenterY := state.Y + playerSize/2

			dx := projCenterX - playerCenterX
			dy := projCenterY - playerCenterY
			distance := math.Sqrt(dx*dx + dy*dy)

			// 当たり判定の半径 = (プレイヤーサイズ + 投射物サイズ) / 2
			hitRadius := (playerSize + math.Max(proj.Width, proj.Height)) / 2
			if distance < hitRadius {
				// ダメージを与える
				state.HP -= proj.Damage
				if state.HP < 0 {
					state.HP = 0
				}

				log.Printf("Player %s hit by projectile, HP: %d", conn.RemoteAddr().String(), state.HP)

				// 投射物を削除（当たったら即座に削除）
				deadProjectiles = append(deadProjectiles, id)
				hasHit = true
				break
			}
		}
		playerStatesMutex.RUnlock()
	}

	// 削除された投射物を削除
	for _, id := range deadProjectiles {
		delete(projectiles, id)
	}
	projectilesMutex.Unlock()

	// プレイヤー状態を更新（HP変更があった場合）
	if hasHit {
		broadcastPlayerStates()
	}

	// 投射物の位置を全クライアントに送信（投射物が削除された場合も空のリストを送信）
	// 各クライアントに対して、投射物の位置を反転させて送信
	clientsMutex.RLock()
	allClients := make([]*websocket.Conn, 0, len(clients))
	for client := range clients {
		allClients = append(allClients, client)
	}
	clientsMutex.RUnlock()

	for _, targetConn := range allClients {
		// ターゲットクライアントの陣地情報を取得
		var targetIsBottom bool
		playerStatesMutex.RLock()
		if targetState, exists := playerStates[targetConn]; exists {
			targetIsBottom = targetState.IsBottom
		}
		playerStatesMutex.RUnlock()

		projectilesMutex.RLock()
		projData := make([]map[string]interface{}, 0)
		for _, proj := range projectiles {
			var displayY float64

			// ターゲットの視点に応じて投射物の座標を変換
			// 基本的な考え方：
			// - ターゲット自身が発射した投射物は、ターゲットの視点に合わせて変換
			// - 相手が発射した投射物は、ターゲットの視点に合わせて変換（ターゲットとは反対側から発射された投射物は反転）
			targetID := targetConn.RemoteAddr().String()
			if targetID == proj.CasterID {
				// 自分が発射した投射物
				if targetIsBottom {
					// ターゲットが下側にいる場合、自分の座標はそのまま
					displayY = proj.Y
				} else {
					// ターゲットが上側にいる場合、自分の座標を反転（自分の視点では下側にいるため）
					displayY = screenHeight - proj.Y
				}
			} else {
				// 相手が発射した投射物
				// 基本的な考え方：相手は常にターゲットとは反対側にいる
				// - ターゲットが下側の場合、相手は上側（物理座標）にいる
				// - ターゲットが上側の場合、相手は下側（物理座標）にいる
				if targetIsBottom {
					// ターゲットが下側にいる場合、相手は上側（物理座標）にいる
					// プレイヤー1から見ると、プレイヤー2が発射した投射物は上側から下に飛んでくる
					// 物理座標をそのまま送る（反転しない）
					displayY = proj.Y
				} else {
					// ターゲットが上側にいる場合、相手は下側（物理座標）にいる
					// プレイヤー2から見ると、プレイヤー1が発射した投射物は上側から下に飛んでくる
					// 物理座標を反転して送る
					displayY = screenHeight - proj.Y
				}
			}

			projData = append(projData, map[string]interface{}{
				"id":      proj.ID,
				"x":       proj.X,
				"y":       displayY,
				"width":   proj.Width,
				"height":  proj.Height,
				"element": proj.Element,
			})
		}
		projectilesMutex.RUnlock()

		updateMsg := map[string]interface{}{
			"type":        "projectiles",
			"projectiles": projData,
		}

		msgJSON, err := json.Marshal(updateMsg)
		if err == nil {
			if err := safeWriteMessage(targetConn, websocket.TextMessage, msgJSON); err != nil {
				log.Printf("Failed to send projectiles: %v", err)
			}
		}
	}
}

// MP回復処理
func regenerateMP() {
	playerStatesMutex.Lock()
	needsUpdate := false
	for _, state := range playerStates {
		if state.MP < 100 {
			state.MP += 2 // 1秒ごとに2回復
			if state.MP > 100 {
				state.MP = 100
			}
			needsUpdate = true
		}
	}
	playerStatesMutex.Unlock()

	if needsUpdate {
		broadcastPlayerStates()
	}
}

func main() {
	// WebSocketエンドポイント
	http.HandleFunc("/ws", handleConnections)

	// 静的ファイル配信（開発用、本番では別途CDNを使用推奨）
	// 本番環境では、クライアントはVercel/Netlifyなどでホストし、
	// サーバーはWebSocketのみを提供することを推奨
	fs := http.FileServer(http.Dir("../static"))
	http.Handle("/", fs)

	// ゲームループを開始
	go gameLoop()

	// ポートを環境変数から取得（デフォルトは8080）
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	log.Printf("Server starting on :%s", port)
	if err := http.ListenAndServe(":"+port, nil); err != nil {
		log.Fatal("Server error:", err)
	}
}
