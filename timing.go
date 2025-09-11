package qotp

import (
	"errors"
	"log/slog"
	"math"
)

const (
	defaultRTO = 200 * msNano
	minRTO     = 100 * msNano
	maxRTO     = 2000 * msNano

	rttExpiry       = 10 * secondNano
	probeMultiplier = 8

	startupGain = 277
	normalGain  = 100
	drainGain   = 75
	probeGain   = 125

	bwDecThreshold = 3

	dupAckBwReduction = 98
	dupAckGain        = 90

	lossBwReduction = 95

	fallbackInterval = 10 * msNano
	rttDivisor       = 10

	rttInflationHigh     = 150
	rttInflationModerate = 125

	MinDeadLine uint64 = 100 * msNano
	ReadDeadLine uint64 = 30 * secondNano // 30 seconds
)

// Combined measurement state - both RTT and BBR in one struct
type Measurements struct {
	// RTT fields
	srtt   uint64 // Smoothed RTT
	rttvar uint64 // RTT variation

	// BBR fields
	isStartup         bool   // true = startup, false = normal
	rttMinNano        uint64 // Keep lowest RTT samples
	rttMinTimeNano    uint64 // When we observed the lowest RTT sample
	bwMax             uint64 // Bytes per second
	bwDec             uint64
	lastProbeTimeNano uint64 // When we last probed for more bandwidth
	pacingGainPct     uint64 // Current pacing gain (100 = 1.0x, 277 = 2.77x)
	lastReadTimeNano  uint64 // Time of last activity
}

// NewMeasurements creates a new instance with default values
func NewMeasurements() Measurements {
	return Measurements{
		isStartup:      true,
		pacingGainPct:  startupGain,
		rttMinNano:     math.MaxUint64,
		rttMinTimeNano: math.MaxUint64,
	}
}

func (c *Connection) UpdateMeasurements(rttMeasurementNano uint64, bytesAcked uint64, nowNano uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Validation
	if rttMeasurementNano == 0 {
		slog.Warn("cannot update measurements, rtt is 0")
		return
	}
	if bytesAcked == 0 {
		slog.Error("cannot ack 0 bytes")
		return
	}
	if rttMeasurementNano > ReadDeadLine {
		slog.Warn("suspiciously high RTT measurement", "rtt_seconds", rttMeasurementNano/secondNano)
		return
	}
	if nowNano == 0 {
		slog.Warn("invalid timestamp")
		return
	}

	// Update RTT (smoothed RTT and variation)
	if c.srtt == 0 {
		// First measurement
		c.srtt = rttMeasurementNano
		c.rttvar = rttMeasurementNano / 2
	} else {
		// Calculate absolute difference for RTT variation
		var delta uint64
		if rttMeasurementNano > c.srtt {
			delta = rttMeasurementNano - c.srtt
		} else {
			delta = c.srtt - rttMeasurementNano
		}

		// Integer-based updates using exact fractions
		c.rttvar = (c.rttvar*3)/4 + (delta*1)/4
		c.srtt = (c.srtt*7)/8 + (rttMeasurementNano*1)/8
	}

	// Update BBR minimum RTT tracking
	if (nowNano > c.rttMinTimeNano && nowNano-c.rttMinTimeNano >= rttExpiry) ||
		rttMeasurementNano < c.rttMinNano {
		c.rttMinNano = rttMeasurementNano
		c.rttMinTimeNano = nowNano
	}

	// Update BBR bandwidth estimation
	bwCurrent := uint64(0)
	if c.rttMinNano > 0 {
		bwCurrent = (bytesAcked * 1_000_000_000) / c.rttMinNano
	}

	if bwCurrent > c.bwMax {
		c.bwMax = bwCurrent
		c.bwDec = 0
	} else {
		c.bwDec++
	}

	// Initialize probe time on first measurement
	if c.lastProbeTimeNano == 0 {
		c.lastProbeTimeNano = nowNano
	}

	// BBR state-specific behavior
	if c.isStartup {
		if c.bwDec >= bwDecThreshold {
			c.isStartup = false
			c.pacingGainPct = normalGain
		}
	} else {
		// Normal state logic
		rttRatioPct := (c.srtt * 100) / c.rttMinNano

		if rttRatioPct > rttInflationHigh {
			c.pacingGainPct = drainGain
		} else if rttRatioPct > rttInflationModerate {
			c.pacingGainPct = dupAckGain
		} else if nowNano-c.lastProbeTimeNano > c.rttMinNano*probeMultiplier {
			c.pacingGainPct = probeGain
			c.lastProbeTimeNano = nowNano
		} else {
			c.pacingGainPct = normalGain
		}
	}
}

func (c *Connection) rtoNano() uint64 {
	rto := c.srtt + 4*c.rttvar

	switch {
	case rto == 0:
		return defaultRTO
	case rto < minRTO:
		return minRTO
	case rto > maxRTO:
		return maxRTO
	default:
		return rto
	}
}

func (c *Connection) OnDuplicateAck() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.bwMax = c.bwMax * dupAckBwReduction / 100
	c.pacingGainPct = dupAckGain

	if c.isStartup {
		c.isStartup = false
	}
}

func (c *Connection) OnPacketLoss() {
	c.mu.Lock()
	defer c.mu.Unlock()

	slog.Debug("PacketLoss",
		slog.Uint64("bwMax", c.bwMax),
		slog.Uint64("newBwMax", c.bwMax*lossBwReduction/100),
		slog.Uint64("gain", c.pacingGainPct),
		slog.Bool("startup", c.isStartup),
	)

	c.bwMax = c.bwMax * lossBwReduction / 100
	c.pacingGainPct = normalGain
	c.isStartup = false
}

func (c *Connection) CalcPacingInterval(packetSize uint64) uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.bwMax == 0 {
		if c.srtt > 0 {
			return c.srtt / rttDivisor
		}
		return fallbackInterval
	}

	adjustedBandwidth := (c.bwMax * c.pacingGainPct) / 100
	if adjustedBandwidth == 0 {
		return fallbackInterval
	}

	return (packetSize * 1_000_000_000) / adjustedBandwidth
}

func backoff(rtoNano uint64, rtoNr int) (uint64, error) {
	if rtoNr <= 0 {
		return 0, errors.New("backoff requires a positive rto number")
	}
	if rtoNr > 5 {
		return 0, errors.New("max retry attempts (4) exceeded")
	}

	for i := 1; i < rtoNr; i++ {
		rtoNano = rtoNano * 2
	}

	return rtoNano, nil
}
