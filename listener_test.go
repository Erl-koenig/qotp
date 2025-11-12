package qotp

import (
	"crypto/ecdh"
	"crypto/rand"
	"fmt"
	"testing"
	"github.com/stretchr/testify/assert"
)

var (
	testPrvSeed1   = [32]byte{1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1}
	testPrvSeed2   = [32]byte{2, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 2}
	testPrvKey1, _ = ecdh.X25519().NewPrivateKey(testPrvSeed1[:])
	testPrvKey2, _ = ecdh.X25519().NewPrivateKey(testPrvSeed2[:])

	hexPubKey1 = fmt.Sprintf("0x%x", testPrvKey1.PublicKey().Bytes())
	hexPubKey2 = fmt.Sprintf("0x%x", testPrvKey2.PublicKey().Bytes())
)

func TestNewListener(t *testing.T) {
	// Test case 1: Create a new listener with a valid address
	listener, err := Listen(WithListenAddr("127.0.0.1:8080"), WithSeed(testPrvSeed1))
	defer func() {
		err := listener.Close()
		assert.Nil(t, err)
	}()
	if err != nil {
		t.Errorf("Expected no error, but got: %v", err)
	}
	if listener == nil {
		t.Errorf("Expected a listener, but got nil")
	}

	// Test case 2: Create a new listener with an invalid address
	_, err = Listen(WithListenAddr("127.0.0.1:99999"), WithSeed(testPrvSeed1))
	if err == nil {
		t.Errorf("Expected an error, but got nil")
	}
}

func TestNewStream(t *testing.T) {
	// Test case 1: Create a new multi-stream with a valid remote address
	listener, err := Listen(WithListenAddr("127.0.0.1:9080"), WithSeed(testPrvSeed1))
	defer func() {
		err := listener.Close()
		assert.Nil(t, err)
	}()
	assert.Nil(t, err)
	conn, err := listener.DialWithCryptoString("127.0.0.1:9081", hexPubKey1)
	assert.Nil(t, err)
	if conn == nil {
		t.Errorf("Expected a multi-stream, but got nil")
	}

	// Test case 2: Create a new multi-stream with an invalid remote address
	conn, err = listener.DialWithCryptoString("127.0.0.1:99999", hexPubKey1)
	if conn != nil {
		t.Errorf("Expected nil, but got a multi-stream")
	}

}

func TestClose(t *testing.T) {
	// Test case 1: Close a listener with no multi-streams
	listener, err := Listen(WithListenAddr("127.0.0.1:9080"), WithSeed(testPrvSeed1))
	assert.NoError(t, err)
	// Test case 2: Close a listener with multi-streams
	listener.DialWithCryptoString("127.0.0.1:9081", hexPubKey1)
	err = listener.Close()
	if err != nil {
		t.Errorf("Expected no error, but got: %v", err)
	}
}

func TestStreamWithAdversarialNetwork(t *testing.T) {
	t.SkipNow()
	connA, listenerB, connPair := setupStreamTest(t)
	
	// Set up adversarial network conditions
	connPair.Conn1.latencyNano = 50 * msNano
	connPair.Conn2.latencyNano = 50 * msNano
	
	streamA := connA.Stream(0)
	
	// Generate random test data (10KB)
	testDataSize := 10 * 1024
	testData := make([]byte, testDataSize)
	_, err := rand.Read(testData)
	assert.NoError(t, err)
	
	// Write data
	n, err := streamA.Write(testData)
	assert.NoError(t, err)
	assert.Equal(t, testDataSize, n)
	
	var streamB *Stream
	receivedData := []byte{}
	maxIterations := 1000
	dropCounter := 0
	
	for i := 0; i < maxIterations; i++ {
		// Sender flushes
		minPacing := connA.listener.Flush(connPair.Conn1.localTime)
		connPair.Conn1.localTime += max(minPacing, 10*msNano)
		
		// Drop every 2nd packet (50% loss)
		if connPair.nrOutgoingPacketsSender() > 0 {
			nPackets := connPair.nrOutgoingPacketsSender()
			pattern := make([]int, nPackets)
			for j := 0; j < nPackets; j++ {
				dropCounter++
				if dropCounter%2 == 0 {
					pattern[j] = -1 // drop
				} else {
					pattern[j] = 1 // deliver
				}
			}
			_, err = connPair.senderToRecipient(pattern...)
			assert.NoError(t, err)
		}
		
		// Receiver processes
		s, err := listenerB.Listen(MinDeadLine, connPair.Conn2.localTime)
		assert.NoError(t, err)
		if s != nil {
			streamB = s
		}
		
		// Try to read available data
		if streamB != nil {
			data, err := streamB.Read()
			if err == nil && len(data) > 0 {
				receivedData = append(receivedData, data...)
			}
		}
		
		// ALWAYS flush receiver to send ACKs (even if streamB is nil)
		minPacing = listenerB.Flush(connPair.Conn2.localTime)
		connPair.Conn2.localTime += max(minPacing, 10*msNano)
		
		// Apply same loss pattern to ACKs
		if connPair.nrOutgoingPacketsReceiver() > 0 {
			nPackets := connPair.nrOutgoingPacketsReceiver()
			pattern := make([]int, nPackets)
			for j := 0; j < nPackets; j++ {
				dropCounter++
				if dropCounter%2 == 0 {
					pattern[j] = -1
				} else {
					pattern[j] = 1
				}
			}
			_, err = connPair.recipientToSender(pattern...)
			assert.NoError(t, err)
		}
		
		// Check if we received all data
		if len(receivedData) >= testDataSize {
			t.Logf("Transfer completed in %d iterations", i+1)
			break
		}
	}
	
	// Verify data integrity
	assert.Equal(t, testDataSize, len(receivedData), "should receive all data")
	assert.Equal(t, testData, receivedData, "received data should match sent data")
}