package udp

import (
	"crypto/rand"
	"errors"
	"io"
	"log"
	"net"
	"time"

	"github.com/beka-birhanu/vinom-client/service/i"
)

type ClientOption func(*ClientSocketManager)

var (
	ErrInvalidRecordType            = errors.New("invalid record type")
	ErrInsecureEncryptionKeySize    = errors.New("insecure encryption key size")
	ErrClientSessionNotFound        = errors.New("client session not found")
	ErrClientAddressIsNotRegistered = errors.New("client address is not registered")
	ErrClientNotFound               = errors.New("client not found")
	ErrMinimumPayloadSizeLimit      = errors.New("minimum payload size limit")
	ErrMaximumPayloadSizeLimit      = errors.New("maximum payload size limit")
	ErrClientCookieIsInvalid        = errors.New("client cookie is invalid")
	ErrInvalidPayloadBodySize       = errors.New("invalid payload body size")
)

const (
	ClientHelloRecordType byte = 1 << iota
	HelloVerifyRecordType
	ServerHelloRecordType
	PingRecordType
	PongRecordType

	defaultReadBufferSize int = 2048

	minimumPayloadSize  int = 3
	insecureSymmKeySize int = 32 // A symmetric key smaller than 256 bits is insecure. 256 bits = 32 bytes in size.
)

// Incoming bytes are parsed into the record struct
type record struct {
	Type byte
	Body []byte
}

// rawRecord is sent to the rawRecords channel when a new payload is received
type rawRecord struct {
	payload []byte
	addr    *net.UDPAddr
}

// ClientSocketManager manages the client-server connection and related operations.
type ClientSocketManager struct {
	conn               *net.UDPConn       // Conn represents the UDP connection to the server.
	logger             *log.Logger        // Logger is used to log messages and errors.
	onConnectionSucces func()             // OnConnectionSucces is a callback function executed when the connection succeeds.
	encoder            i.SocketEncoder    // Encoder is an implementation of Encoder used to encode and decode messages.
	readBufferSize     int                // Maximum buffer size for incoming bytes.
	rawRecords         chan rawRecord     // RawRecords is a channel for processing raw records.
	asymmCrypto        i.Asymmetric       // AsymmCrypto is an implementation of asymmetric encryption.
	serverAsymmPubKey  []byte             // ServerAsymmPubKey is the server's public key for asymmetric encryption.
	symmCrypto         i.Symmetric        // SymmCrypto is an implementation of symmetric encryption.
	clientSymmKey      []byte             // ClientSymmKey is the client's symmetric encryption key.
	authToken          []byte             // AuthToken is the authentication token used for secure communication.
	sessionID          []byte             // SessionID is the identifier for the current session.
	handshakeRandom    []byte             // HandshakeRandom is used during the handshake process.
	pingTicker         *time.Ticker       // PingTicker schedules periodic ping requests.
	pingInterval       time.Duration      // PingInterval is the duration between ping requests.
	pingStopSignal     chan bool          // PingStopSignal stops the ping routine.
	onPingResult       func(int64)        // PingResultCallback is called upon receiving a ping result.
	stopSignal         chan bool          // StopSignal stops the ClientSocketManager.
	onServerResponse   func(byte, []byte) // Callback function to call when server sends message besides handshake and pong.
}

// ClientConfig defines the configuration settings required for a client to connect to a server.
type ClientConfig struct {
	ServerAddr         *net.UDPAddr       // ServerAddr is the UDP address of the server.
	Encoder            i.SocketEncoder    // Encoder is an implementation of Encoder to encode and decode messages.
	AsymmCrypto        i.Asymmetric       // AsymmCrypto is an implementation of asymmetric encryption.
	ServerAsymmPubKey  []byte             // ServerAsymmPubKey is the server's public key for asymmetric encryption.
	SymmCrypto         i.Symmetric        // SymmCrypto is an implementation of symmetric encryption.
	ClientSymmKey      []byte             // ClientSymmKey is the client's symmetric encryption key.
	OnConnectionSucces func()             // OnConnectionSucces is a callback function executed when the connection succeeds.
	OnServerResponse   func(byte, []byte) // Callback function to call when server sends message besides handshake and pong.
	OnPingResult       func(int64)        // PingResultCallback is called upon receiving a ping result.
}

// NewClientServerManager creates a new instance of ClientServerManager.
func NewClientServerManager(c ClientConfig, options ...ClientOption) (*ClientSocketManager, error) {
	conn, err := net.DialUDP("udp", nil, c.ServerAddr)
	if err != nil {
		return nil, err
	}

	manager := &ClientSocketManager{
		conn:               conn,
		encoder:            c.Encoder,
		asymmCrypto:        c.AsymmCrypto,
		serverAsymmPubKey:  c.ServerAsymmPubKey,
		symmCrypto:         c.SymmCrypto,
		clientSymmKey:      c.ClientSymmKey,
		rawRecords:         make(chan rawRecord),
		onConnectionSucces: c.OnConnectionSucces,
		onPingResult:       c.OnPingResult,
		pingStopSignal:     make(chan bool, 1),
		onServerResponse:   c.OnServerResponse,
		stopSignal:         make(chan bool, 1),
	}

	for _, opt := range options {
		opt(manager)
	}

	if manager.readBufferSize == 0 {
		manager.readBufferSize = defaultReadBufferSize
	}

	if manager.pingInterval == 0 {
		manager.pingInterval = time.Second
	}

	if manager.logger == nil {
		// Discard logging if no logger is set
		manager.logger = log.New(io.Discard, "", 0)
	}

	return manager, nil
}

// Connect establishes the initial handshake with the server.
func (c *ClientSocketManager) Connect(authToken []byte) error {
	// Reset pev connection data.
	c.sessionID = []byte{}
	c.authToken = authToken

	// Stop prev ping routine.
	if c.pingTicker != nil {
		c.pingStopSignal <- true
		c.pingTicker.Stop()
	}

	c.pingTicker = time.NewTicker(c.pingInterval)
	go c.requestPing()

	_ = c.conn.SetDeadline(time.Time{})

	clientHello := c.encoder.NewHandshakeRecord()
	random := make([]byte, 32)
	_, err := rand.Read(random)
	if err != nil {
		return err
	}

	c.handshakeRandom = random
	clientHello.SetRandom(random)
	clientHello.SetKey(c.clientSymmKey)

	clientHelloPayload, err := c.encoder.MarshalHandshake(clientHello)
	if err != nil {
		return err
	}

	clientHelloPayload, err = c.asymmCrypto.Encrypt(clientHelloPayload, c.serverAsymmPubKey)
	if err != nil {
		return err
	}

	clientHelloMessage := append([]byte{ClientHelloRecordType}, clientHelloPayload...)
	_, err = c.conn.Write(clientHelloMessage)
	if err != nil {
		c.logger.Printf("error while encoding client hello record: %s", err)
		return err
	}

	c.rawRecords = make(chan rawRecord)
	go c.handleRawRecords()

	c.stopSignal = make(chan bool, 1)
	for {
		select {
		case <-c.stopSignal:
			break

		default:
			buf := make([]byte, c.readBufferSize+1) // Intentionally create more space than allowed for checking
			n, addr, err := c.conn.ReadFromUDP(buf)
			if err != nil {
				if errors.Is(err, net.ErrClosed) {
					continue
				}

				c.logger.Printf("error while reading from udp: %s", err)
				continue
			} else if n > c.readBufferSize {
				c.logger.Println(ErrMaximumPayloadSizeLimit)
				continue
			}
			c.rawRecords <- rawRecord{
				payload: buf[0:n],
				addr:    addr,
			}
		}
	}
}

func (c *ClientSocketManager) Disconnect() {
	defer c.logger.Println("disconnected")
	c.logger.Println("disconnecting...")

	_ = c.conn.SetReadDeadline(time.Unix(0, 1))
	c.stopSignal <- true
	c.pingStopSignal <- true
	c.pingTicker.Stop()
	c.sessionID = []byte{}
	close(c.rawRecords)
}

func (c *ClientSocketManager) handleRawRecords() {
	for r := range c.rawRecords {
		c.handleRawRecord(r.payload)
	}
}

// handleRawRecord processes incoming raw records and takes action based on their type.
func (c *ClientSocketManager) handleRawRecord(payload []byte) {
	if len(payload) < minimumPayloadSize {
		c.logger.Println(ErrMinimumPayloadSizeLimit)
		return
	}

	record, err := parseRecord(payload)
	if err != nil {
		c.logger.Printf("error while parsing record: %s", err)
		return
	}

	switch record.Type {
	case HelloVerifyRecordType:
		c.handleHelloVerifyRecord(record)
	case ServerHelloRecordType:
		c.handleServerHelloRecord(record)
	case PongRecordType:
		c.handlePongRecord(record)
	default:
		c.handleCustomRecord(record)
	}
}

// handleHelloVerifyRecord processes a "HelloVerify" record in the DTLS handshake process.
func (c *ClientSocketManager) handleHelloVerifyRecord(record *record) {
	payload, err := c.symmCrypto.Decrypt(record.Body, c.clientSymmKey)
	if err != nil {
		c.logger.Printf("error while decrypting hello verify record: %s", err)
		return
	}

	helloVerify, err := c.encoder.UnmarshalHandshake(payload)
	if err != nil {
		c.logger.Printf("error while decoding hello verify record: %s", err)
		return
	}

	clientHello := c.encoder.NewHandshakeRecord()
	clientHello.SetCookie(helloVerify.GetCookie())
	clientHello.SetRandom(c.handshakeRandom)
	clientHello.SetKey(c.clientSymmKey)
	clientHello.SetTimestamp(time.Now().UnixNano() / int64(time.Millisecond))
	encryptedToken, err := c.symmCrypto.Encrypt(c.authToken, c.clientSymmKey)
	if err != nil {
		c.logger.Printf("error while encrypting auth token: %s", err)
		return
	}
	clientHello.SetToken(encryptedToken)
	clientHelloPayload, err := c.encoder.MarshalHandshake(clientHello)
	if err != nil {
		c.logger.Printf("error while encoding client hello record: %s", err)
		return
	}

	clientHelloPayload, err = c.asymmCrypto.Encrypt(clientHelloPayload, c.serverAsymmPubKey)
	if err != nil {
		c.logger.Printf("error while encrypting client hello record: %s", err)
		return
	}

	clientHelloMessage := append([]byte{ClientHelloRecordType}, clientHelloPayload...)
	_, err = c.conn.Write(clientHelloMessage)
	if err != nil {
		c.logger.Printf("error while encoding client hello record: %s", err)
		return
	}
}

// handleHelloVerifyRecord processes a "ServerHello" record in the DTLS handshake process.
func (c *ClientSocketManager) handleServerHelloRecord(record *record) {
	payload, err := c.symmCrypto.Decrypt(record.Body, c.clientSymmKey)
	if err != nil {
		c.logger.Printf("error while decrypting hello verify record: %s", err)
		return
	}

	serverHello, err := c.encoder.UnmarshalHandshake(payload)
	if err != nil {
		c.logger.Printf("error while decoding hello verify record: %s", err)
		return
	}

	c.sessionID = serverHello.GetSessionId()
	c.onConnectionSucces()
}

func (c *ClientSocketManager) handlePongRecord(record *record) {
	payload, err := c.symmCrypto.Decrypt(record.Body, c.clientSymmKey)
	if err != nil {
		c.logger.Printf("error while decrypting hello verify record: %s", err)
		return
	}

	pong, err := c.encoder.UnmarshalPong(payload)
	if err != nil {
		c.logger.Printf("error while decoding hello verify record: %s", err)
		return
	}

	go c.onPingResult(pong.GetReceivedAt() - pong.GetPingSentAt())
}

func (c *ClientSocketManager) handleCustomRecord(r *record) {
	payload, err := c.symmCrypto.Decrypt(r.Body, c.clientSymmKey)
	if err != nil {
		c.logger.Printf("error while decrypting custom record: %s", err)
		return
	}

	go c.onServerResponse(r.Type, payload)
}

func (c *ClientSocketManager) requestPing() {
	for {
		select {
		case <-c.pingStopSignal:
			break
		case <-c.pingTicker.C:
			if len(c.sessionID) == 0 { // If no connection has been setup yet.
				continue
			}

			ping := c.encoder.NewPingRecord()
			ping.SetSentAt(time.Now().UnixNano() / int64(time.Millisecond))

			pingMessage, err := c.encoder.MarshalPing(ping)
			if err != nil {
				c.logger.Printf("error while marshaling ping record: %s", err)
				return
			}

			err = c.SendToServer(PingRecordType, pingMessage)
			if err != nil {
				c.logger.Printf("error while sending to server: %s", err)
				return
			}
		}
	}
}

// SendToServer Encrypts and sendes message of type t to server.
func (c *ClientSocketManager) SendToServer(t byte, message []byte) error {
	messageToSend := c.sessionID
	messageToSend = append(messageToSend, message...)
	messageToSend, err := c.symmCrypto.Encrypt(messageToSend, c.clientSymmKey)
	if err != nil {
		return err
	}

	messageToSend = append([]byte{t}, messageToSend...)
	_, err = c.conn.Write(messageToSend)
	return err
}

// SetOnServerResponse updates onServerResponse func.
func (c *ClientSocketManager) SetOnServerResponse(f func(byte, []byte)) {
	c.onServerResponse = f
}

// SetOnPingResult updates onServerResponse func.
func (c *ClientSocketManager) SetOnPingResult(f func(int64)) {
	c.onPingResult = f
}

// parseRecord parses a byte slice into a record struct.
//
// The input format depends on the record type:
//   - For most records: [type, body]
//   - For specific types (e.g., HandshakeClientHelloRecordType): [type, bodysize (2 bytes), body, extra]
func parseRecord(r []byte) (*record, error) {
	if len(r) < 2 {
		return nil, ErrInvalidPayloadBodySize
	}

	return &record{
		Type: r[0],
		Body: r[1:],
	}, nil
}

// ClientWithReadBufferSize sets the read buffer size for the ClientSocketManager.
func ClientWithReadBufferSize(bs int) ClientOption {
	return func(c *ClientSocketManager) {
		c.readBufferSize = bs
	}
}

// ClientWithPingInterval sets the ping interval for the ClientSocketManager.
func ClientWithPingInterval(d time.Duration) ClientOption {
	return func(c *ClientSocketManager) {
		c.pingInterval = d
	}
}

// ClientWithLogger sets the logger for the ClientSocketManager.
func ClientWithLogger(l *log.Logger) ClientOption {
	return func(c *ClientSocketManager) {
		c.logger = l
	}
}
