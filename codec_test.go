package qotp

import (
	"bytes"
	"crypto/ecdh"
	"encoding/binary"
	"net/netip"
	"testing"

	"github.com/stretchr/testify/assert"
)

var (
	seed1 = [32]byte{1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1}
	seed2 = [32]byte{2, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 2}
	seed3 = [32]byte{3, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1}
	seed4 = [32]byte{4, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 2}
	seed5 = [32]byte{5, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1}
	seed6 = [32]byte{6, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 2}

	prvIdAlice, _     = ecdh.X25519().NewPrivateKey(seed1[:])
	prvIdBob, _       = ecdh.X25519().NewPrivateKey(seed2[:])
	prvEpAlice, _     = ecdh.X25519().NewPrivateKey(seed3[:])
	prvEpBob, _       = ecdh.X25519().NewPrivateKey(seed4[:])
	prvEpAliceRoll, _ = ecdh.X25519().NewPrivateKey(seed5[:])
	prvEpBobRoll, _   = ecdh.X25519().NewPrivateKey(seed6[:])
)

// Helper functions
func createTestConnection(isSender, withCrypto, handshakeDone bool) *Conn {
	conn := &Conn{	
		isSenderOnInit:       isSender,
		isWithCryptoOnInit:   withCrypto,
		isHandshakeDoneOnRcv: handshakeDone,
		snCrypto: 0,
		mtu:      1400,
		pubKeyIdRcv: prvIdBob.PublicKey(),
		prvKeyEpSnd: prvEpAlice,
		listener:     &Listener{prvKeyId: prvIdAlice},
		rcv:          NewReceiveBuffer(1000),
		streams:      NewLinkedMap[uint32, *Stream](),
		sharedSecret: bytes.Repeat([]byte{1}, 32),
	}

	if !isSender {
		conn.pubKeyIdRcv = prvIdAlice.PublicKey()
		conn.pubKeyEpRcv = prvEpAlice.PublicKey()
	}

	if handshakeDone {
		conn.pubKeyEpRcv = prvEpBob.PublicKey()
	}

	return conn
}

func createTestListeners() (*Listener, *Listener) {
	lAlice := &Listener{
		connMap:  NewLinkedMap[uint64, *Conn](),
		prvKeyId: prvIdAlice,
	}
	lBob := &Listener{
		connMap:  NewLinkedMap[uint64, *Conn](),
		prvKeyId: prvIdBob,
	}
	return lAlice, lBob
}

func createTestData(size int) []byte {
	testData := make([]byte, size)
	for i := 0; i < len(testData); i++ {
		testData[i] = byte(i % 256)
	}
	return testData
}

func getTestRemoteAddr() netip.AddrPort {
	a, _ := netip.ParseAddr("127.0.0.1")
	return netip.AddrPortFrom(a, uint16(8080))
}

// Closed States Tests
func TestCodecStreamClosed(t *testing.T) {
	conn := createTestConnection(true, false, true)
	stream := conn.Stream(1)
	stream.Close()

	p := stream.payloadHeader(0, nil)
	output, err := conn.encode(p, []byte("test data"), stream.msgType())
	assert.NotNil(t, output)
	assert.NoError(t, err)
}

func TestCodecConnectionClosed(t *testing.T) {
	conn := createTestConnection(true, false, true)
	stream := conn.Stream(1)
	stream.conn.Close()

	p := stream.payloadHeader(0, nil)
	output, err := conn.encode(p, []byte("test data"), stream.msgType())
	assert.NotNil(t, output)
	assert.NoError(t, err)
}

// Overhead Calculation Tests - Updated to use CalcMaxOverhead function
func TestCodecOverheadInitSndNoData(t *testing.T) {
	overhead := calcCryptoOverhead(InitSnd, nil, 100)
	assert.Equal(t, -1, overhead)
}

func TestCodecOverheadInitRcvNoAck(t *testing.T) {
	overhead := calcCryptoOverhead(InitRcv, nil, 100)
	expected := calcProtoOverhead(false, false) + MinInitRcvSizeHdr + FooterDataSize
	assert.Equal(t, expected, overhead)
}

func TestCodecOverheadInitCryptoSnd(t *testing.T) {
	overhead := calcCryptoOverhead(InitCryptoSnd, nil, 100)
	expected := calcProtoOverhead(false, false) + MinInitCryptoSndSizeHdr + FooterDataSize + MsgInitFillLenSize
	assert.Equal(t, expected, overhead)
}

func TestCodecOverheadInitCryptoRcv(t *testing.T) {
	overhead := calcCryptoOverhead(InitCryptoRcv, nil, 100)
	expected := calcProtoOverhead(false, false) + MinInitCryptoRcvSizeHdr + FooterDataSize
	assert.Equal(t, expected, overhead)
}

func TestCodecOverheadDataLargeAckOffset(t *testing.T) {
	ack := &Ack{offset: 0xFFFFFF + 1}
	overhead := calcCryptoOverhead(Data, ack, 100)
	expected := calcProtoOverhead(true, true) + MinDataSizeHdr + FooterDataSize
	assert.Equal(t, expected, overhead)
}

func TestCodecOverheadDataLargeDataOffset(t *testing.T) {
	overhead := calcCryptoOverhead(Data, nil, 0xFFFFFF+1)
	expected := calcProtoOverhead(false, true) + MinDataSizeHdr + FooterDataSize
	assert.Equal(t, expected, overhead)
}

func TestCodecOverheadDataSmallOffsets(t *testing.T) {
	ack := &Ack{offset: 1000}
	overhead := calcCryptoOverhead(Data, ack, 2000)
	expected := calcProtoOverhead(true, false) + MinDataSizeHdr + FooterDataSize
	assert.Equal(t, expected, overhead)
}

func TestCodecOverheadDataNoAck(t *testing.T) {
	overhead := calcCryptoOverhead(Data, nil, 2000)
	expected := calcProtoOverhead(false, false) + MinDataSizeHdr + FooterDataSize
	assert.Equal(t, expected, overhead)
}

// Data Size Tests
func TestCodecDataSizeZero(t *testing.T) {
	lAlice, lBob := createTestListeners()

	connAlice := createTestConnection(true, true, false)
	connAlice.snd = NewSendBuffer(rcvBufferCapacity, nil)
	connAlice.rcv = NewReceiveBuffer(12000)

	connId := binary.LittleEndian.Uint64(prvEpAlice.PublicKey().Bytes())
	lAlice.connMap.Put(connId, connAlice)
	connAlice.connId = connId

	streamAlice := &Stream{conn: connAlice}
	testData := createTestData(0)

	p := streamAlice.payloadHeader(0, nil)
	encoded, err := connAlice.encode(p, testData, streamAlice.msgType())
	assert.NoError(t, err)
	assert.NotNil(t, encoded)

	connBob, payload, msgType, err := lBob.decode(encoded, getTestRemoteAddr())
	assert.NoError(t, err)

	if msgType == InitCryptoRcv {
		p, u, err := DecodePayload(payload)
		s, err := connBob.decode(p, u, 0, 0)
		assert.NoError(t, err)
		assert.NotNil(t, s)
	}
}

func TestCodecDataSizeOne(t *testing.T) {
	lAlice, lBob := createTestListeners()

	connAlice := createTestConnection(true, true, false)
	connAlice.snd = NewSendBuffer(rcvBufferCapacity, nil)
	connAlice.rcv = NewReceiveBuffer(12000)

	connId := binary.LittleEndian.Uint64(prvEpAlice.PublicKey().Bytes())
	lAlice.connMap.Put(connId, connAlice)
	connAlice.connId = connId

	streamAlice := &Stream{conn: connAlice}
	testData := createTestData(1)

	p := streamAlice.payloadHeader(0, nil)
	encoded, err := connAlice.encode(p, testData, streamAlice.msgType())
	assert.NoError(t, err)
	assert.NotNil(t, encoded)

	connBob, payload, _, err := lBob.decode(encoded, getTestRemoteAddr())
	assert.NoError(t, err)

	p, u, err := DecodePayload(payload)
	s, err := connBob.decode(p, u, 0, 0)
	assert.NoError(t, err)
	_, rb := s.conn.rcv.RemoveOldestInOrder(s.streamID)
	assert.Equal(t, testData, rb)
}

func TestCodecDataSizeHundred(t *testing.T) {
	lAlice, lBob := createTestListeners()

	connAlice := createTestConnection(true, true, false)
	connAlice.snd = NewSendBuffer(rcvBufferCapacity, nil)
	connAlice.rcv = NewReceiveBuffer(12000)

	connId := binary.LittleEndian.Uint64(prvEpAlice.PublicKey().Bytes())
	lAlice.connMap.Put(connId, connAlice)
	connAlice.connId = connId

	streamAlice := &Stream{conn: connAlice}
	testData := createTestData(100)

	p := streamAlice.payloadHeader(0, nil)
	encoded, err := connAlice.encode(p, testData, streamAlice.msgType())
	assert.NoError(t, err)
	assert.NotNil(t, encoded)

	connBob, payload, _, err := lBob.decode(encoded, getTestRemoteAddr())
	assert.NoError(t, err)

	p, u, err := DecodePayload(payload)
	s, err := connBob.decode(p, u, 0, 0)
	assert.NoError(t, err)
	_, rb := s.conn.rcv.RemoveOldestInOrder(s.streamID)
	assert.Equal(t, testData, rb)
}

func TestCodecDataSizeThousand(t *testing.T) {
	lAlice, lBob := createTestListeners()

	connAlice := createTestConnection(true, true, false)
	connAlice.snd = NewSendBuffer(rcvBufferCapacity, nil)
	connAlice.rcv = NewReceiveBuffer(12000)

	connId := binary.LittleEndian.Uint64(prvEpAlice.PublicKey().Bytes())
	lAlice.connMap.Put(connId, connAlice)
	connAlice.connId = connId

	streamAlice := &Stream{conn: connAlice}
	testData := createTestData(1000)

	p := streamAlice.payloadHeader(0, nil)
	encoded, err := connAlice.encode(p, testData, streamAlice.msgType())
	assert.NoError(t, err)
	assert.NotNil(t, encoded)

	connBob, payload, _, err := lBob.decode(encoded, getTestRemoteAddr())
	assert.NoError(t, err)

	p, u, err := DecodePayload(payload)
	s, err := connBob.decode(p, u, 0, 0)
	assert.NoError(t, err)
	_, rb := s.conn.rcv.RemoveOldestInOrder(s.streamID)
	assert.Equal(t, testData, rb)
}

func TestCodecDataSizeLarge(t *testing.T) {
	lAlice, lBob := createTestListeners()

	connAlice := createTestConnection(true, true, false)
	connAlice.snd = NewSendBuffer(rcvBufferCapacity, nil)
	connAlice.rcv = NewReceiveBuffer(12000)

	connId := binary.LittleEndian.Uint64(prvEpAlice.PublicKey().Bytes())
	lAlice.connMap.Put(connId, connAlice)
	connAlice.connId = connId

	streamAlice := &Stream{conn: connAlice}
	testData := createTestData(1295)

	p := streamAlice.payloadHeader(0, nil)
	encoded, err := connAlice.encode(p, testData, streamAlice.msgType())
	assert.NoError(t, err)
	assert.NotNil(t, encoded)

	connBob, payload, _, err := lBob.decode(encoded, getTestRemoteAddr())
	assert.NoError(t, err)

	p, u, err := DecodePayload(payload)
	s, err := connBob.decode(p, u, 0, 0)
	assert.NoError(t, err)
	_, rb := s.conn.rcv.RemoveOldestInOrder(s.streamID)
	assert.Equal(t, testData, rb)
}

// Full Handshake Test
func TestCodecFullHandshake(t *testing.T) {
	lAlice, lBob := createTestListeners()
	remoteAddr := getTestRemoteAddr()

	// Alice's initial connection
	connAlice := &Conn{
		connId: Uint64(prvEpAlice.PublicKey().Bytes()),
		isSenderOnInit: true,
		snCrypto: 0,
		mtu:      1400,
		prvKeyEpSnd: prvEpAlice,
		listener: lAlice,
		rcv:      NewReceiveBuffer(1000),
		streams:  NewLinkedMap[uint32, *Stream](),
	}
	lAlice.connMap.Put(connAlice.connId, connAlice)

	streamAlice := &Stream{conn: connAlice}

	// Step 1: Alice encodes InitSnd
	p := streamAlice.payloadHeader(0, nil)
	encoded, err := connAlice.encode(p, nil, streamAlice.msgType())
	assert.NoError(t, err)
	assert.NotNil(t, encoded)

	// Step 2: Bob receives and decodes InitSnd
	connBob, _, msgTypeS0, err := lBob.decode(encoded, remoteAddr)
	assert.NoError(t, err)
	assert.NotNil(t, connBob)
	assert.Equal(t, InitSnd, msgTypeS0)

	// Step 3: Bob responds with InitRcv
	streamBob := &Stream{conn: connBob}
	testData := []byte("handshake response")
	p = streamBob.payloadHeader(0, nil)
	encodedR0, err := connBob.encode(p, testData, streamBob.msgType())
	assert.NoError(t, err)
	assert.NotNil(t, encodedR0)

	// Step 4: Alice receives and decodes InitRcv
	c, payload, msgType, err := lAlice.decode(encodedR0, remoteAddr)
	assert.NoError(t, err)
	assert.Equal(t, InitRcv, msgType)

	p, u, err := DecodePayload(payload)
	s, err := c.decode(p, u, 0, 0)
	assert.NoError(t, err)
	_, rb := s.conn.rcv.RemoveOldestInOrder(s.streamID)
	assert.Equal(t, testData, rb)

	// Step 5: Setup for Data message flow after handshake
	connId := binary.LittleEndian.Uint64(prvIdAlice.PublicKey().Bytes()) ^ binary.LittleEndian.Uint64(prvIdBob.PublicKey().Bytes())

	connAlice.isHandshakeDoneOnRcv = true
	connAlice.pubKeyIdRcv = prvIdBob.PublicKey()
	connAlice.pubKeyEpRcv = prvEpBob.PublicKey()
	connAlice.sharedSecret = seed1[:]
	lAlice.connMap.Put(connId, connAlice)

	connBob.isHandshakeDoneOnRcv = true
	connBob.sharedSecret = seed1[:]
	lBob.connMap.Put(connId, connBob)

	// Step 6: Alice sends Data message
	dataMsg := []byte("data message")
	p = streamAlice.payloadHeader(0, nil)
	encoded, err = connAlice.encode(p, dataMsg, streamAlice.msgType())
	assert.NoError(t, err)
	assert.NotNil(t, encoded)

	// Step 7: Bob receives and decodes Data message
	c, payload, msgType, err = lBob.decode(encoded, remoteAddr)
	assert.NoError(t, err)
	assert.NotNil(t, c)
	assert.Equal(t, Data, msgType)

	p, u, err = DecodePayload(payload)
	s, err = c.decode(p, u, 0, 0)
	assert.NoError(t, err)
	_, rb = s.conn.rcv.RemoveOldestInOrder(s.streamID)
	assert.Equal(t, dataMsg, rb)
}

// Sequence Number Tests
func TestCodecSequenceNumberRollover(t *testing.T) {
	conn := createTestConnection(true, false, true)
	conn.snCrypto = (1 << 48) - 2 // Near rollover
	conn.epochCryptoSnd = 0

	stream := &Stream{conn: conn}

	// First encode should succeed and rollover snCrypto
	p := stream.payloadHeader(0, nil)
	_, err := conn.encode(p, []byte("test"), Data)
	assert.NoError(t, err)
	assert.Equal(t, uint64((1<<48)-1), conn.snCrypto)

	// Second encode should trigger rollover
	p = stream.payloadHeader(0, nil)
	_, err = conn.encode(p, []byte("test"), Data)
	assert.NoError(t, err)
	assert.Equal(t, uint64(0), conn.snCrypto)
	assert.Equal(t, uint64(1), conn.epochCryptoSnd)
}

func TestCodecSequenceNumberExhaustion(t *testing.T) {
	conn := createTestConnection(true, false, true)
	conn.snCrypto = (1 << 48) - 1
	conn.epochCryptoSnd = (1 << 47) - 1 // Max epoch

	stream := &Stream{conn: conn}

	// Should fail with exhaustion error
	p := stream.payloadHeader(0, nil)
	_, err := conn.encode(p, []byte("test"), Data)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "exhausted")
}

// Error Tests
func TestCodecInvalidMessageType(t *testing.T) {
	conn := createTestConnection(true, false, true)
	stream := &Stream{conn: conn}

	// Test with invalid message type
	p := stream.payloadHeader(0, nil)
	_, err := conn.encode(p, []byte("test"), MsgType(99))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unknown message type")
}

func TestCodecDecodeEmptyBuffer(t *testing.T) {
	l := &Listener{
		connMap:  NewLinkedMap[uint64, *Conn](),
		prvKeyId: prvIdAlice,
	}

	_, _, _, err := l.decode([]byte{}, getTestRemoteAddr())
	assert.Error(t, err)
}

func TestCodecDecodeInvalidHeader(t *testing.T) {
	l := &Listener{
		connMap:  NewLinkedMap[uint64, *Conn](),
		prvKeyId: prvIdAlice,
	}

	_, _, _, err := l.decode([]byte{0xFF}, getTestRemoteAddr())
	assert.Error(t, err)
}

func TestCodecDecodeConnectionNotFoundInitRcv(t *testing.T) {
	l := &Listener{
		connMap:  NewLinkedMap[uint64, *Conn](),
		prvKeyId: prvIdAlice,
	}

	buffer := append([]byte{byte(InitRcv)}, make([]byte, 15)...)
	_, _, _, err := l.decode(buffer, getTestRemoteAddr())
	assert.Error(t, err)
}

func TestCodecDecodeConnectionNotFoundData(t *testing.T) {
	l := &Listener{
		connMap:  NewLinkedMap[uint64, *Conn](),
		prvKeyId: prvIdAlice,
	}

	buffer := append([]byte{byte(Data)}, make([]byte, 15)...)
	_, _, _, err := l.decode(buffer, getTestRemoteAddr())
	assert.Error(t, err)
}

// Message Type Logic Tests
func TestCodecMsgTypeInitCryptoSnd(t *testing.T) {
	conn := createTestConnection(true, true, false)
	stream := &Stream{conn: conn}

	msgType := stream.msgType()
	assert.Equal(t, InitCryptoSnd, msgType)
}

func TestCodecMsgTypeInitCryptoRcv(t *testing.T) {
	conn := createTestConnection(false, true, false)
	stream := &Stream{conn: conn}

	msgType := stream.msgType()
	assert.Equal(t, InitCryptoRcv, msgType)
}

func TestCodecMsgTypeInitSnd(t *testing.T) {
	conn := createTestConnection(true, false, false)
	stream := &Stream{conn: conn}

	msgType := stream.msgType()
	assert.Equal(t, InitSnd, msgType)
}

func TestCodecMsgTypeInitRcv(t *testing.T) {
	conn := createTestConnection(false, false, false)
	stream := &Stream{conn: conn}

	msgType := stream.msgType()
	assert.Equal(t, InitRcv, msgType)
}

func TestCodecMsgTypeData(t *testing.T) {
	conn := createTestConnection(true, false, true)
	stream := &Stream{conn: conn}

	msgType := stream.msgType()
	assert.Equal(t, Data, msgType)
}
