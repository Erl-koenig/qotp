package qotp

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func encodePayload(payload *PayloadHeader, data []byte) []byte {
	encoded, _ := EncodePayload(payload, data)
	return encoded
}

func mustDecodePayload(t *testing.T, encoded []byte) (*PayloadHeader, []byte) {
	decoded, data, err := DecodePayload(encoded)
	require.NoError(t, err)
	return decoded, data
}

func roundTrip(t *testing.T, payload *PayloadHeader, data []byte) (*PayloadHeader, []byte) {
	encoded := encodePayload(payload, data)
	return mustDecodePayload(t, encoded)
}

func assertPayloadEqual(t *testing.T, expected, actual *PayloadHeader) {
	assert.Equal(t, expected.StreamID, actual.StreamID)
	assert.Equal(t, expected.StreamOffset, actual.StreamOffset)
	assert.Equal(t, expected.IsClose, actual.IsClose)

	if expected.Ack == nil {
		assert.Nil(t, actual.Ack)
	} else {
		require.NotNil(t, actual.Ack)
		assert.Equal(t, expected.Ack.streamID, actual.Ack.streamID)
		assert.Equal(t, expected.Ack.offset, actual.Ack.offset)
		assert.Equal(t, expected.Ack.len, actual.Ack.len)

		encoded := EncodeRcvWindow(expected.Ack.rcvWnd)
		expectedDecoded := DecodeRcvWindow(encoded)
		assert.Equal(t, expectedDecoded, actual.Ack.rcvWnd)
	}
}

// =============================================================================
// Type 01: DATA no ACK
// =============================================================================

func TestDataNoAck(t *testing.T) {
	original := &PayloadHeader{
		StreamID:     12345,
		StreamOffset: 100,
	}
	originalData := []byte("test data")

	decoded, decodedData := roundTrip(t, original, originalData)

	assertPayloadEqual(t, original, decoded)
	assert.Equal(t, originalData, decodedData)
}

func TestDataNoAckEmpty(t *testing.T) {
	// Type 01: empty data with data header 0 -> ping
	original := &PayloadHeader{
		StreamID:     1,
		StreamOffset: 0,
	}

	decoded, decodedData := roundTrip(t, original, []byte{})

	assertPayloadEqual(t, original, decoded)
	assert.Empty(t, decodedData)
}

// =============================================================================
// Type 00: DATA with ACK
// =============================================================================

func TestDataWithAckAndData(t *testing.T) {
	original := &PayloadHeader{
		StreamID:     1,
		StreamOffset: 100,
		Ack:          &Ack{streamID: 10, offset: 200, len: 300, rcvWnd: 1000},
	}
	originalData := []byte("payload")

	decoded, decodedData := roundTrip(t, original, originalData)

	assertPayloadEqual(t, original, decoded)
	assert.Equal(t, originalData, decodedData)
}

func TestDataWithAckPing(t *testing.T) {
	// Type 00: empty data + data header 0 -> ping
	original := &PayloadHeader{
		StreamID:     1,
		StreamOffset: 100,
		Ack:          &Ack{streamID: 1, offset: 50, len: 0, rcvWnd: 1000},
	}

	decoded, decodedData := roundTrip(t, original, []byte{})

	assertPayloadEqual(t, original, decoded)
	assert.Empty(t, decodedData)
}

func TestDataWithAckNoDataHeader(t *testing.T) {
	// Type 00: empty data + empty data header -> regular ack
	original := &PayloadHeader{
		Ack: &Ack{streamID: 10, offset: 200, len: 300, rcvWnd: 1000},
	}

	decoded, decodedData := roundTrip(t, original, nil)

	assertPayloadEqual(t, original, decoded)
	assert.Nil(t, decodedData)
}

// =============================================================================
// Type 10: CLOSE with ACK
// =============================================================================

func TestCloseWithAck(t *testing.T) {
	original := &PayloadHeader{
		IsClose:      true,
		StreamID:     1,
		StreamOffset: 9999,
		Ack:          &Ack{streamID: 1, offset: 123456, len: 10, rcvWnd: 1000},
	}
	originalData := []byte("closing")

	decoded, decodedData := roundTrip(t, original, originalData)

	assertPayloadEqual(t, original, decoded)
	assert.Equal(t, originalData, decodedData)
}

// =============================================================================
// Type 11: CLOSE no ACK
// =============================================================================

func TestCloseNoAck(t *testing.T) {
	original := &PayloadHeader{
		IsClose:      true,
		StreamID:     1,
		StreamOffset: 100,
	}

	decoded, _ := roundTrip(t, original, []byte{})

	assertPayloadEqual(t, original, decoded)
}

// =============================================================================
// Offset Size Tests
// =============================================================================

func TestOffset24Bit(t *testing.T) {
	original := &PayloadHeader{
		StreamID:     1,
		StreamOffset: 0xFFFFFF,
	}

	decoded, _ := roundTrip(t, original, []byte{})
	assertPayloadEqual(t, original, decoded)
}

func TestOffset48Bit(t *testing.T) {
	original := &PayloadHeader{
		StreamID:     1,
		StreamOffset: 0x1000000,
	}

	decoded, _ := roundTrip(t, original, []byte{})
	assertPayloadEqual(t, original, decoded)
}

func TestAckOffset48Bit(t *testing.T) {
	original := &PayloadHeader{
		StreamID:     5,
		StreamOffset: 0x1000000,
		Ack:          &Ack{streamID: 50, offset: 0x1000000, len: 200, rcvWnd: 5000},
	}

	decoded, _ := roundTrip(t, original, []byte{})
	assertPayloadEqual(t, original, decoded)
}

func TestMixedOffsets(t *testing.T) {
	// Data offset 48-bit, ACK offset 24-bit
	original := &PayloadHeader{
		StreamID:     1,
		StreamOffset: 0x1000000,
		Ack:          &Ack{streamID: 10, offset: 100, len: 50, rcvWnd: 1000},
	}

	decoded, _ := roundTrip(t, original, []byte{})
	assertPayloadEqual(t, original, decoded)
}

// =============================================================================
// Error Tests
// =============================================================================

func TestErrorBelowMinSize(t *testing.T) {
	testCases := []int{0, 1, 7}
	for _, size := range testCases {
		data := make([]byte, size)
		_, _, err := DecodePayload(data)
		assert.Error(t, err)
	}
}

func TestErrorInvalidVersion(t *testing.T) {
	data := make([]byte, 8)
	data[0] = 0x1F // Invalid version (bits 0-4 = 31)

	_, _, err := DecodePayload(data)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "version")
}

func TestErrorInsufficientData(t *testing.T) {
	// Type 00 with ACK needs at least 11 bytes for 24-bit
	data := make([]byte, 10)
	data[0] = 0x00 // Type 00

	_, _, err := DecodePayload(data)
	assert.Error(t, err)
}

// =============================================================================
// RcvWindow Tests
// =============================================================================

func TestRcvWindowRoundTrip(t *testing.T) {
	testCases := []uint64{
		0, 512, 1024, 2048, 4096, 8192, 16384, 32768,
		65536, 131072, 262144, 524288, 1048576, 1073741824,
	}

	for _, input := range testCases {
		encoded := EncodeRcvWindow(input)
		decoded := DecodeRcvWindow(encoded)
		assert.LessOrEqual(t, input, decoded)
	}
}

func TestRcvWindowEdgeCases(t *testing.T) {
	assert.Equal(t, uint8(0), EncodeRcvWindow(0))
	assert.Equal(t, uint8(1), EncodeRcvWindow(1))
	assert.Equal(t, uint8(1), EncodeRcvWindow(128))
	assert.Equal(t, uint8(1), EncodeRcvWindow(255))
	assert.Equal(t, uint8(2), EncodeRcvWindow(256))

	assert.Equal(t, uint64(0), DecodeRcvWindow(0))
	assert.Equal(t, uint64(128), DecodeRcvWindow(1))
	assert.Equal(t, uint64(256), DecodeRcvWindow(2))
}

func TestRcvWindowMonotonic(t *testing.T) {
	prev := DecodeRcvWindow(2)
	for i := uint8(3); i <= 254; i++ {
		curr := DecodeRcvWindow(i)
		assert.Greater(t, curr, prev)
		prev = curr
	}
}

func TestRcvWindowMax(t *testing.T) {
	encoded := EncodeRcvWindow(1 << 63)
	assert.Equal(t, uint8(255), encoded)

	decoded := DecodeRcvWindow(255)
	assert.Greater(t, decoded, uint64(800_000_000_000))
	assert.Less(t, decoded, uint64(900_000_000_000))
}

// =============================================================================
// Additional Tests
// =============================================================================

func TestOverheadCalculation(t *testing.T) {
	assert.Equal(t, 8, calcProtoOverhead(false, false, false)) // No ACK, 24-bit
	assert.Equal(t, 11, calcProtoOverhead(false, true, false)) // No ACK, 48-bit
	assert.Equal(t, 18, calcProtoOverhead(true, false, false)) // ACK, 24-bit
	assert.Equal(t, 24, calcProtoOverhead(true, true, false))  // ACK, 48-bit
	assert.Equal(t, 11, calcProtoOverhead(true, false, true))  // ACK, no data header, 24-bit
	assert.Equal(t, 14, calcProtoOverhead(true, true, true))   // ACK, no data header, 48-bit
}

func TestLargeData(t *testing.T) {
	largeData := make([]byte, 65000)
	for i := range largeData {
		largeData[i] = byte(i % 256)
	}

	original := &PayloadHeader{
		StreamID:     1,
		StreamOffset: 0,
	}

	decoded, decodedData := roundTrip(t, original, largeData)
	assertPayloadEqual(t, original, decoded)
	assert.Equal(t, largeData, decodedData)
}

func TestAckZeroLength(t *testing.T) {
	original := &PayloadHeader{
		StreamID:     1,
		StreamOffset: 100,
		Ack:          &Ack{streamID: 1, offset: 100, len: 0, rcvWnd: 1000},
	}

	decoded, _ := roundTrip(t, original, []byte{})
	assertPayloadEqual(t, original, decoded)
	assert.Equal(t, uint16(0), decoded.Ack.len)
}
