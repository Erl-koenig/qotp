package tomtp

import (
	"crypto/ecdh"
	"errors"
	"fmt"
	"log/slog"

	"golang.org/x/crypto/chacha20"
	"golang.org/x/crypto/chacha20poly1305"
)

type MsgType int8

const (
	InitSnd MsgType = iota
	InitRcv
	InitCryptoSnd
	InitCryptoRcv
	DataRot
	Data
)

const (
	Version = 0
	MacSize = 16
	SnSize  = 6 // Sequence number Size is 48bit / 6 bytes
	//MinPayloadSize is the minimum payload Size in bytes. We need at least 8 bytes as
	// 8 + the MAC Size (16 bytes) is 24 bytes, which is used as the input for
	// sealing with chacha20poly1305.NewX().
	MinPayloadSize = 8
	PubKeySize     = 32

	HeaderSize         = 1
	ConnIdSize         = 8
	MsgInitFillLenSize = 2

	HeaderConnIdSize         = HeaderSize + ConnIdSize
	MinInitSndSize   = startMtu
	MinInitRcvSize   = HeaderConnIdSize + (3 * PubKeySize) + SnSize + MacSize
	MinInitCryptoSndSize = HeaderConnIdSize + (3 * PubKeySize) + SnSize + MsgInitFillLenSize + MacSize
	MinInitCryptoRcvSize = HeaderConnIdSize + (2 * PubKeySize) + SnSize + MacSize
	MinDataRotSize      = HeaderConnIdSize + PubKeySize + SnSize + MacSize
	MinDataSize       = HeaderConnIdSize + SnSize + MacSize
)

type Message struct {
	MsgType      MsgType
	SnConn       uint64
	PayloadRaw   []byte
	SharedSecret []byte
}

// ************************************* Encoder *************************************

func fillHeaderKey(header []byte, msgType MsgType, pubKeyEpSnd *ecdh.PublicKey, pubKeyEpRcv *ecdh.PublicKey) {
	// Write version
	header[0] = (Version << 3) | uint8(msgType)

	connId := Uint64(pubKeyEpSnd.Bytes())
	if msgType == DataRot || msgType == Data {
		connId = connId ^ Uint64(pubKeyEpRcv.Bytes())
	}

	PutUint64(header[HeaderSize:], connId)
}

func EncodeInitSnd(
	pubKeyIdSnd *ecdh.PublicKey,
	prvKeyEpSnd *ecdh.PrivateKey,
	prvKeyEpSndRollover *ecdh.PrivateKey) (encData []byte) {

	if pubKeyIdSnd == nil || prvKeyEpSnd == nil || prvKeyEpSndRollover == nil {
		panic("handshake keys cannot be nil")
	}

	// Create the buffer with the correct size
	headerCryptoDataBuffer := make([]byte, startMtu)

	fillHeaderKey(headerCryptoDataBuffer, InitSnd, prvKeyEpSnd.PublicKey(), nil)

	// Directly copy the isSender's public key to the buffer following the connection ID
	copy(headerCryptoDataBuffer[HeaderConnIdSize:], pubKeyIdSnd.Bytes())

	// Directly copy the ephemeral public key to the buffer following the isSender's public key
	copy(headerCryptoDataBuffer[HeaderConnIdSize+PubKeySize:], prvKeyEpSnd.PublicKey().Bytes())

	// Copy isSender's ephemeral rollover public key
	copy(headerCryptoDataBuffer[HeaderConnIdSize+2*PubKeySize:], prvKeyEpSndRollover.PublicKey().Bytes())

	return headerCryptoDataBuffer
}

func EncodeInitRcv(
	pubKeyIdRcv *ecdh.PublicKey,
	pubKeyIdSnd *ecdh.PublicKey,
	pubKeyEpRcv *ecdh.PublicKey,
	prvKeyEpSnd *ecdh.PrivateKey,
	prvKeyEpSndRollover *ecdh.PrivateKey,
	rawData []byte) (encData []byte, err error) {

	if pubKeyIdRcv == nil || pubKeyIdSnd == nil || pubKeyEpRcv == nil || prvKeyEpSnd == nil ||
		prvKeyEpSndRollover == nil {
		panic("handshake keys cannot be nil")
	}

	// Create the buffer with the correct size, INIT_HANDSHAKE_R0 has 3 public keys
	headerCryptoBuffer := make([]byte, HeaderConnIdSize+(3*PubKeySize))

	fillHeaderKey(headerCryptoBuffer, InitRcv, pubKeyEpRcv, nil)

	// Directly copy the isSender's public key to the buffer following the connection ID
	copy(headerCryptoBuffer[HeaderConnIdSize:], pubKeyIdSnd.Bytes())

	// Directly copy the ephemeral public key to the buffer following the isSender's public key
	copy(headerCryptoBuffer[HeaderConnIdSize+PubKeySize:], prvKeyEpSnd.PublicKey().Bytes())

	// Copy isSender's ephemeral rollover public key
	copy(headerCryptoBuffer[HeaderConnIdSize+(2*PubKeySize):], prvKeyEpSndRollover.PublicKey().Bytes())

	// Perform ECDH for initial encryption
	sharedSecret, err := prvKeyEpSnd.ECDH(pubKeyEpRcv)

	slog.Debug("EncodeInitHandshakeR0 shared secret:", slog.Any("sharedSecret1", sharedSecret))

	if err != nil {
		return nil, err
	}

	// Encrypt and write dataToSend
	return chainedEncrypt(0, false, sharedSecret, headerCryptoBuffer, rawData)

}

func EncodeInitCryptoSnd(
	pubKeyIdRcv *ecdh.PublicKey,
	pubKeyIdSnd *ecdh.PublicKey,
	prvKeyEpSnd *ecdh.PrivateKey,
	prvKeyEpSndRollover *ecdh.PrivateKey,
	rawData []byte) (encData []byte, err error) {

	if pubKeyIdRcv == nil || pubKeyIdSnd == nil || prvKeyEpSnd == nil || prvKeyEpSndRollover == nil {
		panic("handshake keys cannot be nil")
	}

	if len(rawData) < MinPayloadSize {
		return nil, errors.New("packet dataToSend too short")
	}

	// Create the buffer with the correct size, INIT_WITH_CRYPTO_S0 has 3 public keys
	headerCryptoBuffer := make([]byte, HeaderConnIdSize+(3*PubKeySize))

	fillHeaderKey(headerCryptoBuffer, InitCryptoSnd, prvKeyEpSnd.PublicKey(), nil)

	// Directly copy the isSender's public key to the buffer following the connection ID
	copy(headerCryptoBuffer[HeaderConnIdSize:], pubKeyIdSnd.Bytes())

	// Directly copy the ephemeral public key to the buffer following the isSender's public key
	copy(headerCryptoBuffer[HeaderConnIdSize+PubKeySize:], prvKeyEpSnd.PublicKey().Bytes())

	// Copy isSender's ephemeral rollover public key
	copy(headerCryptoBuffer[HeaderConnIdSize+2*PubKeySize:], prvKeyEpSndRollover.PublicKey().Bytes())

	// Encrypt and write dataToSend
	fillLen := uint16(startMtu - MinInitCryptoSndSize - len(rawData))

	// Create payload with filler length and filler if needed
	payloadWithFiller := make([]byte, 2+int(fillLen)+len(rawData)) // +2 for filler length
	// Add filler length
	PutUint16(payloadWithFiller, fillLen)
	// After the filler, copy the dataToSend
	copy(payloadWithFiller[2+int(fillLen):], rawData)

	// Perform ECDH for initial encryption
	noPerfectForwardSharedSecret, err := prvKeyEpSnd.ECDH(pubKeyIdRcv)

	if err != nil {
		return nil, err
	}

	return chainedEncrypt(0, true, noPerfectForwardSharedSecret, headerCryptoBuffer, payloadWithFiller)
}

func EncodeInitCryptoRcv(
	pubKeyIdRcv *ecdh.PublicKey,
	pubKeyIdSnd *ecdh.PublicKey,
	pubKeyEpRcv *ecdh.PublicKey,
	prvKeyEpSnd *ecdh.PrivateKey,
	prvKeyEpSndRollover *ecdh.PrivateKey,
	rawData []byte) (encData []byte, err error) {

	if pubKeyIdRcv == nil || pubKeyIdSnd == nil || pubKeyEpRcv == nil ||
		prvKeyEpSnd == nil || prvKeyEpSndRollover == nil {
		panic("handshake keys cannot be nil")
	}

	if len(rawData) < MinPayloadSize {
		return nil, errors.New("packet dataToSend too short")
	}

	// Create the buffer with the correct size, INIT_WITH_CRYPTO_R0 has 2 public keys
	headerCryptoBuffer := make([]byte, HeaderConnIdSize+(2*PubKeySize))

	fillHeaderKey(headerCryptoBuffer, InitCryptoRcv, pubKeyEpRcv, nil)

	// Directly copy the ephemeral public key to the buffer following the isSender's public key
	copy(headerCryptoBuffer[HeaderConnIdSize:], prvKeyEpSnd.PublicKey().Bytes())

	// Copy isSender's ephemeral rollover public key
	copy(headerCryptoBuffer[HeaderConnIdSize+PubKeySize:], prvKeyEpSndRollover.PublicKey().Bytes())

	// Perform ECDH for initial encryption
	sharedSecret, err := prvKeyEpSnd.ECDH(pubKeyEpRcv)

	if err != nil {
		return nil, err
	}

	// Encrypt and write dataToSend
	return chainedEncrypt(0, false, sharedSecret, headerCryptoBuffer, rawData)
}

func EncodeDataRot(
	pubKeyEpSnd *ecdh.PublicKey,
	pubKeyEpRcv *ecdh.PublicKey,
	isSender bool,
	prvKeyEpSndRollover *ecdh.PrivateKey,
	rawData []byte) (encData []byte, err error) {

	if pubKeyEpSnd == nil || pubKeyEpRcv == nil || prvKeyEpSndRollover == nil {
		panic("pubKeyEpSnd/pubKeyEpRcv keys cannot be nil")
	}

	if len(rawData) < MinPayloadSize {
		return nil, errors.New("packet dataToSend too short")
	}

	// Create the buffer with the correct size, DATA_0 has 1 public key
	headerCryptoBuffer := make([]byte, HeaderConnIdSize+PubKeySize)

	fillHeaderKey(headerCryptoBuffer, DataRot, pubKeyEpSnd, pubKeyEpRcv)

	// Directly copy the ephemeral public key to the buffer following the connection ID
	copy(headerCryptoBuffer[HeaderConnIdSize:], prvKeyEpSndRollover.PublicKey().Bytes())

	// Perform ECDH for second encryption
	perfectForwardSharedSecret, err := prvKeyEpSndRollover.ECDH(pubKeyEpRcv)
	if err != nil {
		return nil, err
	}

	// Encrypt and write dataToSend
	return chainedEncrypt(0, isSender, perfectForwardSharedSecret, headerCryptoBuffer, rawData)
}

func EncodeData(
	pubKeyEpSnd *ecdh.PublicKey,
	pubKeyEpRcv *ecdh.PublicKey,
	isSender bool,
	sharedSecret []byte,
	sn uint64,
	rawData []byte) (encData []byte, err error) {

	if pubKeyEpSnd == nil || pubKeyEpRcv == nil || sharedSecret == nil {
		panic("pubKeyEpSnd/pubKeyEpRcv keys cannot be nil")
	}

	if len(rawData) < MinPayloadSize {
		return nil, errors.New("packet dataToSend too short")
	}

	// Create the buffer with the correct size, DATA_0 has no public key
	headerBuffer := make([]byte, HeaderConnIdSize)

	fillHeaderKey(headerBuffer, Data, pubKeyEpSnd, pubKeyEpRcv)

	// Encrypt and write dataToSend
	return chainedEncrypt(sn, isSender, sharedSecret, headerBuffer, rawData)
}

func chainedEncrypt(snConn uint64, isSender bool, sharedSecret []byte, headerAndCrypto []byte, rawData []byte) (fullMessage []byte, err error) {
	if len(rawData) < 8 {
		return nil, errors.New("dataToSend too short")
	}
	if snConn >= (1 << (SnSize * 8)) {
		return nil, fmt.Errorf("serial number is not a 48-bit value")
	}

	// Rest remains zero filled

	snConnSer := make([]byte, SnSize)
	PutUint48(snConnSer, snConn)
	nonceDet := make([]byte, chacha20poly1305.NonceSize)

	// If isSender, place in first half; if receiver, place in second half
	offset := 0
	if !isSender {
		offset = SnSize
	}
	copy(nonceDet[offset:], snConnSer)

	aead, err := chacha20poly1305.New(sharedSecret)
	if err != nil {
		return nil, err
	}
	encData := aead.Seal(nil, nonceDet, rawData, headerAndCrypto)

	fullMessage = make([]byte, len(headerAndCrypto)+SnSize+len(encData))
	copy(fullMessage, headerAndCrypto)

	aeadSn, err := chacha20poly1305.NewX(sharedSecret)
	if err != nil {
		return nil, err
	}

	nonceRand := encData[0:24]
	encSn := aeadSn.Seal(nil, nonceRand, snConnSer, nil)
	copy(fullMessage[len(headerAndCrypto):], encSn[:SnSize])
	copy(fullMessage[len(headerAndCrypto)+SnSize:], encData)

	// Write the full message in one operation
	return fullMessage, nil
}

// ************************************* Decoder *************************************

func decodeHeader(encData []byte) (connId uint64, msgType MsgType, err error) {
	// Read the header byte and connId
	if len(encData) < HeaderConnIdSize {
		return 0, DataRot, errors.New("header needs to be at least 9 bytes")
	}

	header := encData[0]
	version := header >> 3

	if version != Version {
		return 0, DataRot, errors.New("unsupported version version")
	}

	msgType = MsgType(header & 0x07)
	connId = Uint64(encData[HeaderSize:HeaderConnIdSize])

	return connId, msgType, nil
}

func DecodeInitSnd(encData []byte, prvKeyEpRcv *ecdh.PrivateKey) (
	pubKeyIdSnd *ecdh.PublicKey,
	pubKeyEpSnd *ecdh.PublicKey,
	pubKeyEpSndRollover *ecdh.PublicKey,
	m *Message,
	err error) {

	if len(encData) < MinInitSndSize {
		return nil, nil, nil, nil, errors.New("size is below minimum init")
	}

	pubKeyIdSnd, err = ecdh.X25519().NewPublicKey(encData[HeaderConnIdSize : HeaderConnIdSize+PubKeySize])
	if err != nil {
		return nil, nil, nil, nil, err
	}

	pubKeyEpSnd, err = ecdh.X25519().NewPublicKey(encData[HeaderConnIdSize+PubKeySize : HeaderConnIdSize+2*PubKeySize])
	if err != nil {
		return nil, nil, nil, nil, err
	}

	pubKeyEpSndRollover, err = ecdh.X25519().NewPublicKey(
		encData[HeaderConnIdSize+2*PubKeySize : HeaderConnIdSize+3*PubKeySize])
	if err != nil {
		return nil, nil, nil, nil, err
	}

	sharedSecret, err := prvKeyEpRcv.ECDH(pubKeyEpSnd)

	if err != nil {
		return nil, nil, nil, nil, err
	}

	return pubKeyIdSnd, pubKeyEpSnd, pubKeyEpSndRollover, &Message{
		MsgType:      InitSnd,
		SharedSecret: sharedSecret,
		SnConn:       0,
	}, nil
}

func DecodeInitRcv(encData []byte, prvKeyEpSnd *ecdh.PrivateKey) (
	pubKeyIdRcv *ecdh.PublicKey,
	pubKeyEpRcv *ecdh.PublicKey,
	pubKeyEpRcvRollover *ecdh.PublicKey,
	m *Message,
	err error) {

	if len(encData) < MinInitRcvSize {
		return nil, nil, nil, nil, errors.New("size is below minimum init reply")
	}

	pubKeyIdRcv, err = ecdh.X25519().NewPublicKey(encData[HeaderConnIdSize : HeaderConnIdSize+PubKeySize])
	if err != nil {
		return nil, nil, nil, nil, err
	}

	pubKeyEpRcv, err = ecdh.X25519().NewPublicKey(encData[HeaderConnIdSize+PubKeySize : HeaderConnIdSize+2*PubKeySize])
	if err != nil {
		return nil, nil, nil, nil, err
	}

	pubKeyEpRcvRollover, err = ecdh.X25519().NewPublicKey(
		encData[HeaderConnIdSize+2*PubKeySize : HeaderConnIdSize+(3*PubKeySize)])
	if err != nil {
		return nil, nil, nil, nil, err
	}

	sharedSecret, err := prvKeyEpSnd.ECDH(pubKeyEpRcv)

	slog.Debug("EncodeInitHandshakeR0 shared secret:", slog.Any("sharedSecret2", sharedSecret))

	if err != nil {
		return nil, nil, nil, nil, err
	}

	snConn, decryptedData, err := chainedDecrypt(
		false,
		sharedSecret,
		encData[0:HeaderConnIdSize+(3*PubKeySize)],
		encData[HeaderConnIdSize+(3*PubKeySize):],
	)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	if snConn != 0 {
		return nil, nil, nil, nil, errors.New("sn must be 0")
	}

	return pubKeyIdRcv, pubKeyEpRcv, pubKeyEpRcvRollover, &Message{
		MsgType:      InitRcv,
		PayloadRaw:   decryptedData,
		SharedSecret: sharedSecret,
		SnConn:       snConn,
	}, nil

}

func DecodeInitCryptoSnd(
	encData []byte,
	prvKeyIdRcv *ecdh.PrivateKey,
	prvKeyEpRcv *ecdh.PrivateKey) (
	pubKeyIdSnd *ecdh.PublicKey,
	pubKeyEpSnd *ecdh.PublicKey,
	pubKeyEpSndRollover *ecdh.PublicKey,
	m *Message,
	err error) {

	if len(encData) < MinInitCryptoSndSize {
		return nil, nil, nil, nil, errors.New("size is below minimum init")
	}

	pubKeyIdSnd, err = ecdh.X25519().NewPublicKey(encData[HeaderConnIdSize : HeaderConnIdSize+PubKeySize])
	if err != nil {
		return nil, nil, nil, nil, err
	}

	pubKeyEpSnd, err = ecdh.X25519().NewPublicKey(encData[HeaderConnIdSize+PubKeySize : HeaderConnIdSize+2*PubKeySize])
	if err != nil {
		return nil, nil, nil, nil, err
	}

	pubKeyEpSndRollover, err = ecdh.X25519().NewPublicKey(
		encData[HeaderConnIdSize+2*PubKeySize : HeaderConnIdSize+3*PubKeySize])
	if err != nil {
		return nil, nil, nil, nil, err
	}

	noPerfectForwardSharedSecret, err := prvKeyIdRcv.ECDH(pubKeyEpSnd)

	if err != nil {
		return nil, nil, nil, nil, err
	}

	snConn, decryptedData, err := chainedDecrypt(
		true,
		noPerfectForwardSharedSecret,
		encData[0:HeaderConnIdSize+(3*PubKeySize)],
		encData[HeaderConnIdSize+(3*PubKeySize):],
	)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	if snConn != 0 {
		return nil, nil, nil, nil, errors.New("sn must be 0")
	}

	sharedSecret, err := prvKeyEpRcv.ECDH(pubKeyEpSnd)

	if err != nil {
		return nil, nil, nil, nil, err
	}

	// Extract actual dataToSend - Remove filler_length and filler
	fillerLen := Uint16(decryptedData)
	actualData := decryptedData[2+int(fillerLen):]

	return pubKeyIdSnd, pubKeyEpSnd, pubKeyEpSndRollover, &Message{
		MsgType:      InitCryptoSnd,
		PayloadRaw:   actualData,
		SharedSecret: sharedSecret,
		SnConn:       snConn,
	}, nil
}

// DecodeInitCryptoRcv is decoded by the isSender
func DecodeInitCryptoRcv(
	encData []byte,
	prvKeyEpSnd *ecdh.PrivateKey) (
	pubKeyEpRcv *ecdh.PublicKey,
	pubKeyEpRcvRollover *ecdh.PublicKey,
	m *Message,
	err error) {

	if len(encData) < MinInitCryptoRcvSize {
		return nil, nil, nil, errors.New("size is below minimum init reply")
	}

	pubKeyEpRcv, err = ecdh.X25519().NewPublicKey(encData[HeaderConnIdSize : HeaderConnIdSize+PubKeySize])
	if err != nil {
		return nil, nil, nil, err
	}

	pubKeyEpRcvRollover, err = ecdh.X25519().NewPublicKey(
		encData[HeaderConnIdSize+PubKeySize : HeaderConnIdSize+2*PubKeySize])
	if err != nil {
		return nil, nil, nil, err
	}

	sharedSecret, err := prvKeyEpSnd.ECDH(pubKeyEpRcv)
	if err != nil {
		return nil, nil, nil, err
	}

	snConn, decryptedData, err := chainedDecrypt(
		false,
		sharedSecret,
		encData[0:HeaderConnIdSize+(2*PubKeySize)],
		encData[HeaderConnIdSize+(2*PubKeySize):],
	)
	if err != nil {
		return nil, nil, nil, err
	}
	if snConn != 0 {
		return nil, nil, nil, errors.New("sn must be 0")
	}

	return pubKeyEpRcv, pubKeyEpRcvRollover, &Message{
		MsgType:      InitCryptoRcv,
		PayloadRaw:   decryptedData,
		SharedSecret: sharedSecret,
		SnConn:       snConn,
	}, nil
}

func DecodeDataRot(
	encData []byte,
	isSender bool,
	prvKeyEpSnd *ecdh.PrivateKey) (
	pubKeyEpRollover *ecdh.PublicKey, m *Message, err error) {

	if len(encData) < MinDataRotSize {
		return nil, nil, errors.New("size is below minimum Data0")
	}

	pubKeyEpRollover, err = ecdh.X25519().NewPublicKey(
		encData[HeaderConnIdSize : HeaderConnIdSize+PubKeySize])
	if err != nil {
		return nil, nil, err
	}

	sharedSecret, err := prvKeyEpSnd.ECDH(pubKeyEpRollover)
	if err != nil {
		return nil, nil, err
	}

	snConn, decryptedData, err := chainedDecrypt(
		isSender,
		sharedSecret,
		encData[0:HeaderConnIdSize+PubKeySize],
		encData[HeaderConnIdSize+PubKeySize:],
	)
	if err != nil {
		return nil, nil, err
	}
	if snConn != 0 {
		return nil, nil, errors.New("sn must be 0")
	}

	return pubKeyEpRollover, &Message{
		MsgType:      DataRot,
		PayloadRaw:   decryptedData,
		SharedSecret: sharedSecret,
		SnConn:       snConn,
	}, nil
}

func DecodeData(
	encData []byte,
	isSender bool,
	sharedSecret []byte) (*Message, error) {

	if len(encData) < MinDataSize {
		return nil, errors.New("size is below minimum")
	}

	snConn, decryptedData, err := chainedDecrypt(
		isSender,
		sharedSecret,
		encData[0:HeaderConnIdSize],
		encData[HeaderConnIdSize:],
	)
	if err != nil {
		return nil, err
	}

	return &Message{
		MsgType:    Data,
		PayloadRaw: decryptedData,
		SnConn:     snConn,
	}, nil
}

func chainedDecrypt(isSender bool, sharedSecret []byte, header []byte, encData []byte) (snConn uint64, decryptedData []byte, err error) {
	if len(encData) < 24 { // 8 bytes for encSn + 24 bytes for nonceRand
		return 0, nil, errors.New("encrypted dataToSend too short")
	}

	snConnSer := make([]byte, SnSize)

	encSn := encData[0:SnSize]
	encData = encData[SnSize:]
	nonceRand := encData[:24]
	snConnSer, err = openNoVerify(sharedSecret, nonceRand, encSn, snConnSer)
	if err != nil {
		return 0, nil, err
	}

	nonceDet := make([]byte, chacha20poly1305.NonceSize)

	offset := 0
	if isSender {
		offset = SnSize
	}
	copy(nonceDet[offset:], snConnSer)

	aead, err := chacha20poly1305.New(sharedSecret)
	if err != nil {
		return 0, nil, err
	}

	decryptedData, err = aead.Open(nil, nonceDet, encData, header)
	if err != nil {
		return 0, nil, err
	}

	snConn = Uint48(snConnSer)

	return snConn, decryptedData, nil
}

// inspired by: https://github.com/golang/crypto/blob/master/chacha20poly1305/chacha20poly1305_generic.go
func openNoVerify(sharedSecret []byte, nonce []byte, encoded []byte, snSer []byte) ([]byte, error) {
	s, err := chacha20.NewUnauthenticatedCipher(sharedSecret, nonce)
	if err != nil {
		return nil, err
	}
	s.SetCounter(1) // Set the counter to 1, skipping 32 bytes

	// Decrypt the ciphertext
	s.XORKeyStream(snSer, encoded)

	return snSer, nil
}
