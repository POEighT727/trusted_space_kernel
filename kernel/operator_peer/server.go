package operator_peer

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// PeerConnection 代表与对等运维方的连接
type PeerConnection struct {
	conn           net.Conn
	peerKernelID   string            // 对端内核ID
	peerAddr       string
	connectedAt    time.Time
	lastActive    int64             // 最后活跃时间（Unix时间戳）
	status        int32             // 连接状态：0=未连接，1=已连接，2=正在握手
	reader        *PacketReaderJSON
	writer        *PacketWriterJSON
	closeOnce     sync.Once
	closed        chan struct{}
	onDisconnect  func(kernelID string)
	onMessage     func(msgType PacketType, payload []byte, fromKernelID string)
	mu            sync.RWMutex
	traceID       [16]byte          // 追踪ID
}

// PacketReaderJSON JSON格式数据包读取器
type PacketReaderJSON struct {
	conn   net.Conn
	reader *json.Decoder
}

// NewPacketReaderJSON 创建PacketReader
func NewPacketReaderJSON(conn net.Conn) *PacketReaderJSON {
	return &PacketReaderJSON{
		conn:   conn,
		reader: json.NewDecoder(conn),
	}
}

// ReadPacket 读取JSON格式的数据包
func (r *PacketReaderJSON) ReadPacket() (*Packet, error) {
	// 读取固定头
	header := make([]byte, 28)
	if _, err := io.ReadFull(r.conn, header); err != nil {
		return nil, fmt.Errorf("failed to read header: %w", err)
	}

	// 解析头部长度字段（偏移量8-12）
	length := binary.BigEndian.Uint32(header[8:12])
	if length > MaxPacketSize {
		return nil, fmt.Errorf("payload too large: %d", length)
	}

	// 读取载荷
	payload := make([]byte, length)
	if _, err := io.ReadFull(r.conn, payload); err != nil {
		return nil, fmt.Errorf("failed to read payload: %w", err)
	}

	// 解析追踪ID
	var traceID [16]byte
	copy(traceID[:], header[12:28])

	return &Packet{
		Version: header[4],
		Type:    PacketType(header[5]),
		Flags:   header[6],
		Length:  length,
		TraceID: traceID,
		Payload: payload,
	}, nil
}

// PacketWriterJSON JSON格式数据包写入器
type PacketWriterJSON struct {
	conn   net.Conn
	writer *json.Encoder
	mu     sync.Mutex
}

// NewPacketWriterJSON 创建PacketWriter
func NewPacketWriterJSON(conn net.Conn) *PacketWriterJSON {
	return &PacketWriterJSON{
		conn:   conn,
		writer: json.NewEncoder(conn),
	}
}

// WritePacket 写入JSON格式的数据包
func (w *PacketWriterJSON) WritePacket(packet *Packet) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	// 编码
	data, err := packet.Encode()
	if err != nil {
		return fmt.Errorf("failed to encode packet: %w", err)
	}

	// 写入
	if _, err := w.conn.Write(data); err != nil {
		return fmt.Errorf("failed to write packet: %w", err)
	}

	return nil
}

// P2PServer TCP服务端，负责接收来自其他运维方的连接
type P2PServer struct {
	listenAddr string
	listener   net.Listener
	running    int32
	stopped    chan struct{}

	// 连接管理
	peers   map[string]*PeerConnection // key: peerKernelID
	peersMu sync.RWMutex

	// 回调
	onHandshake          func(conn net.Conn, payload *HandshakePayload) (*HandshakeAckPayload, error)
	onSyncConnectors     func(payload *SyncConnectorsPayload) (*SyncConnectorsAckPayload, error)
	onRelayMessage       func(payload *RelayMessagePayload) (*RelayMessageAckPayload, error)
	onHeartbeat          func(payload *HeartbeatPayload) (*HeartbeatAckPayload, error)
	onPeerConnected      func(kernelID string, conn *PeerConnection)
	onPeerDisconnected   func(kernelID string)
}

// NewP2PServer 创建P2P服务器
func NewP2PServer(listenAddr string) *P2PServer {
	return &P2PServer{
		listenAddr:        listenAddr,
		peers:            make(map[string]*PeerConnection),
		stopped:           make(chan struct{}),
	}
}

// SetHandshakeHandler 设置握手处理回调
func (s *P2PServer) SetHandshakeHandler(h func(conn net.Conn, payload *HandshakePayload) (*HandshakeAckPayload, error)) {
	s.onHandshake = h
}

// SetSyncConnectorsHandler 设置连接器同步处理回调
func (s *P2PServer) SetSyncConnectorsHandler(h func(payload *SyncConnectorsPayload) (*SyncConnectorsAckPayload, error)) {
	s.onSyncConnectors = h
}

// SetRelayMessageHandler 设置消息转发处理回调
func (s *P2PServer) SetRelayMessageHandler(h func(payload *RelayMessagePayload) (*RelayMessageAckPayload, error)) {
	s.onRelayMessage = h
}

// SetHeartbeatHandler 设置心跳处理回调
func (s *P2PServer) SetHeartbeatHandler(h func(payload *HeartbeatPayload) (*HeartbeatAckPayload, error)) {
	s.onHeartbeat = h
}

// SetPeerConnectedHandler 设置对等方连接回调
func (s *P2PServer) SetPeerConnectedHandler(h func(kernelID string, conn *PeerConnection)) {
	s.onPeerConnected = h
}

// SetPeerDisconnectedHandler 设置对等方断开回调
func (s *P2PServer) SetPeerDisconnectedHandler(h func(kernelID string)) {
	s.onPeerDisconnected = h
}

// Start 启动P2P服务器
func (s *P2PServer) Start() error {
	// 解析监听地址
	ln, err := net.Listen("tcp", s.listenAddr)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", s.listenAddr, err)
	}
	s.listener = ln

	atomic.StoreInt32(&s.running, 1)

	log.Printf("[P2P] Server listening on %s", s.listenAddr)

	// 启动接受连接goroutine
	go s.acceptLoop()

	return nil
}

// acceptLoop 接受连接循环
func (s *P2PServer) acceptLoop() {
	for atomic.LoadInt32(&s.running) == 1 {
		conn, err := s.listener.Accept()
		if err != nil {
			select {
			case <-s.stopped:
				return
			default:
				log.Printf("[P2P] Accept error: %v", err)
				continue
			}
		}

		log.Printf("[P2P] New incoming connection from %s", conn.RemoteAddr().String())

		// 处理新连接
		go s.handleConnection(conn)
	}
}

// handleConnection 处理新连接
func (s *P2PServer) handleConnection(conn net.Conn) {
	peer := &PeerConnection{
		conn:        conn,
		peerAddr:    conn.RemoteAddr().String(),
		connectedAt: time.Now(),
		status:      1,
		closed:      make(chan struct{}),
	}

	reader := NewPacketReaderJSON(conn)
	writer := NewPacketWriterJSON(conn)
	peer.reader = reader
	peer.writer = writer

	defer func() {
		peer.Close()
		conn.Close()
	}()

	// 主循环
	for {
		select {
		case <-peer.closed:
			return
		default:
			// 设置读超时
			conn.SetReadDeadline(time.Now().Add(60 * time.Second))

			// 读取数据包
			packet, err := reader.ReadPacket()
			if err != nil {
				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					// 读超时，发送心跳检测
					continue
				}
				log.Printf("[P2P] Read error from %s: %v", conn.RemoteAddr().String(), err)
				return
			}

			// 更新最后活跃时间
			atomic.StoreInt64(&peer.lastActive, time.Now().Unix())

			// 处理消息
			s.handlePacket(peer, packet)
		}
	}
}

// handlePacket 处理数据包
func (s *P2PServer) handlePacket(peer *PeerConnection, packet *Packet) {
	switch packet.Type {
	case PacketTypeHandshake:
		s.handleHandshake(peer, packet)
	case PacketTypeSyncConnectors:
		s.handleSyncConnectors(peer, packet)
	case PacketTypeRelayMessage:
		s.handleRelayMessage(peer, packet)
	case PacketTypeHeartbeat:
		s.handleHeartbeat(peer, packet)
	case PacketTypeDisconnect:
		s.handleDisconnect(peer, packet)
	default:
		log.Printf("[P2P] Unknown packet type: %d", packet.Type)
	}
}

// handleHandshake 处理握手
func (s *P2PServer) handleHandshake(peer *PeerConnection, packet *Packet) {
	var payload HandshakePayload
	if err := JSONUnmarshal(packet.Payload, &payload); err != nil {
		log.Printf("[P2P] Failed to parse handshake payload: %v", err)
		return
	}

	log.Printf("[P2P] Handshake request from kernel %s (%s)", payload.KernelID, payload.KernelAddr)

	peer.mu.Lock()
	peer.peerKernelID = payload.KernelID
	peer.traceID = packet.TraceID
	peer.mu.Unlock()

	// 记录对端信息
	peerAddr := peer.conn.RemoteAddr().String()

	var ackPayload *HandshakeAckPayload
	if s.onHandshake != nil {
		var err error
		ackPayload, err = s.onHandshake(peer.conn, &payload)
		if err != nil {
			ackPayload = &HandshakeAckPayload{
				Success:   false,
				Message:   err.Error(),
				Timestamp: time.Now().Unix(),
			}
		}
	} else {
		ackPayload = &HandshakeAckPayload{
			Success:   true,
			Message:   "OK",
			KernelID:  payload.KernelID,
			Version:   ProtocolVersion,
			Timestamp: time.Now().Unix(),
		}
	}

	// 发送握手响应
	ackData, _ := JSONMarshal(ackPayload)
	peer.mu.RLock()
	traceIDStr := string(peer.traceID[:])
	peer.mu.RUnlock()
	ackPacket := NewPacket(PacketTypeHandshakeAck, traceIDStr, ackData)
	if err := peer.writer.WritePacket(ackPacket); err != nil {
		log.Printf("[P2P] Failed to send handshake ack: %v", err)
		return
	}

	// 注册对等方
	s.peersMu.Lock()
	s.peers[payload.KernelID] = peer
	s.peersMu.Unlock()

	log.Printf("[P2P] Peer registered: %s from %s", payload.KernelID, peerAddr)

	// 触发连接回调
	if s.onPeerConnected != nil {
		s.onPeerConnected(payload.KernelID, peer)
	}
}

// handleSyncConnectors 处理连接器同步
func (s *P2PServer) handleSyncConnectors(peer *PeerConnection, packet *Packet) {
	var payload SyncConnectorsPayload
	if err := JSONUnmarshal(packet.Payload, &payload); err != nil {
		log.Printf("[P2P] Failed to parse sync payload: %v", err)
		return
	}

	log.Printf("[P2P] Sync connectors from %s: %d connectors", payload.SourceKernelID, len(payload.Connectors))

	var ackPayload *SyncConnectorsAckPayload
	if s.onSyncConnectors != nil {
		var err error
		ackPayload, err = s.onSyncConnectors(&payload)
		if err != nil {
			ackPayload = &SyncConnectorsAckPayload{
				Success: false,
				Message: err.Error(),
			}
		}
	} else {
		ackPayload = &SyncConnectorsAckPayload{
			Success:   true,
			Message:   "OK",
			Timestamp: time.Now().Unix(),
		}
	}

	// 发送响应
	ackData, _ := JSONMarshal(ackPayload)
	ackPacket := NewPacket(PacketTypeSyncAck, string(packet.TraceID[:]), ackData)
	if err := peer.writer.WritePacket(ackPacket); err != nil {
		log.Printf("[P2P] Failed to send sync ack: %v", err)
		return
	}
}

// handleRelayMessage 处理消息转发
func (s *P2PServer) handleRelayMessage(peer *PeerConnection, packet *Packet) {
	var payload RelayMessagePayload
	if err := JSONUnmarshal(packet.Payload, &payload); err != nil {
		log.Printf("[P2P] Failed to parse relay payload: %v", err)
		return
	}

	log.Printf("[P2P] Relay message from %s: %s -> %s", payload.SenderKernelID, payload.SenderID, payload.ReceiverID)

	var ackPayload *RelayMessageAckPayload
	if s.onRelayMessage != nil {
		var err error
		ackPayload, err = s.onRelayMessage(&payload)
		if err != nil {
			ackPayload = &RelayMessageAckPayload{
				Success:   false,
				Message:   err.Error(),
				MessageID: payload.MessageID,
			}
		}
	} else {
		ackPayload = &RelayMessageAckPayload{
			Success:     true,
			Message:     "OK",
			MessageID:   payload.MessageID,
			DeliveredAt: time.Now().Unix(),
		}
	}

	// 发送响应
	ackData, _ := JSONMarshal(ackPayload)
	ackPacket := NewPacket(PacketTypeRelayMessageAck, string(packet.TraceID[:]), ackData)
	if err := peer.SendPacket(ackPacket); err != nil {
		log.Printf("[P2P] Failed to send relay ack: %v", err)
		return
	}
}

// GetConn 获取底层TCP连接
func (p *PeerConnection) GetConn() net.Conn {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.conn
}

// GetPeerKernelID 获取对端内核ID
func (p *PeerConnection) GetPeerKernelID() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.peerKernelID
}

// SendPacket 通过连接发送数据包（用于回复）
func (p *PeerConnection) SendPacket(packet *Packet) error {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.writer.WritePacket(packet)
}

// SendRelayMessage 发送中继消息
func (p *PeerConnection) SendRelayMessage(msg *RelayMessagePayload) error {
	payloadData, err := JSONMarshal(msg)
	if err != nil {
		return fmt.Errorf("failed to marshal relay payload: %w", err)
	}
	packet := NewPacket(PacketTypeRelayMessage, fmt.Sprintf("%d", time.Now().UnixNano()), payloadData)
	return p.SendPacket(packet)
}

// handleHeartbeat 处理心跳
func (s *P2PServer) handleHeartbeat(peer *PeerConnection, packet *Packet) {
	var payload HeartbeatPayload
	if err := JSONUnmarshal(packet.Payload, &payload); err != nil {
		log.Printf("[P2P] Failed to parse heartbeat payload: %v", err)
		return
	}

	// 响应心跳
	ackPayload := &HeartbeatAckPayload{
		Success:             true,
		KernelID:            payload.KernelID,
		Timestamp:           time.Now().Unix(),
		ConnectedConnectors: payload.ConnectedConnectors,
		ActivePeers:        payload.ActivePeers,
	}

	ackData, _ := JSONMarshal(ackPayload)
	ackPacket := NewPacket(PacketTypeHeartbeatAck, string(packet.TraceID[:]), ackData)
	if err := peer.writer.WritePacket(ackPacket); err != nil {
		log.Printf("[P2P] Failed to send heartbeat ack: %v", err)
		return
	}
}

// handleDisconnect 处理断开连接
func (s *P2PServer) handleDisconnect(peer *PeerConnection, packet *Packet) {
	peer.mu.RLock()
	kernelID := peer.peerKernelID
	peer.mu.RUnlock()

	log.Printf("[P2P] Received disconnect from %s", kernelID)

	// 移除对等方
	s.peersMu.Lock()
	delete(s.peers, kernelID)
	s.peersMu.Unlock()

	// 触发断开回调
	if s.onPeerDisconnected != nil {
		s.onPeerDisconnected(kernelID)
	}

	peer.Close()
}

// GetPeer 获取指定内核的对等连接
func (s *P2PServer) GetPeer(kernelID string) *PeerConnection {
	s.peersMu.RLock()
	defer s.peersMu.RUnlock()
	return s.peers[kernelID]
}

// ListPeers 列出所有对等连接
func (s *P2PServer) ListPeers() []*PeerConnection {
	s.peersMu.RLock()
	defer s.peersMu.RUnlock()

	peers := make([]*PeerConnection, 0, len(s.peers))
	for _, p := range s.peers {
		peers = append(peers, p)
	}
	return peers
}

// GetPeerCount 获取对等连接数量
func (s *P2PServer) GetPeerCount() int {
	s.peersMu.RLock()
	defer s.peersMu.RUnlock()
	return len(s.peers)
}

// Close 关闭服务器
func (s *P2PServer) Close() error {
	atomic.StoreInt32(&s.running, 0)
	close(s.stopped)

	// 关闭所有对等连接
	s.peersMu.Lock()
	for kernelID, peer := range s.peers {
		peer.Close()
		delete(s.peers, kernelID)
	}
	s.peersMu.Unlock()

	// 关闭监听
	if s.listener != nil {
		return s.listener.Close()
	}
	return nil
}

// Close 关闭对等连接
func (p *PeerConnection) Close() {
	p.closeOnce.Do(func() {
		close(p.closed)
		if p.conn != nil {
			p.conn.Close()
		}
	})
}

// SendMessage 发送消息到对等方
func (p *PeerConnection) SendMessage(msgType PacketType, payload []byte) error {
	packet := NewPacket(msgType, fmt.Sprintf("%d", time.Now().UnixNano()), payload)
	return p.writer.WritePacket(packet)
}

// IsConnected 检查连接是否活跃
func (p *PeerConnection) IsConnected() bool {
	return atomic.LoadInt32(&p.status) == 1
}
