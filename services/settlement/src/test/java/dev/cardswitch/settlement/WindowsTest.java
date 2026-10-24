package dev.cardswitch.settlement;

import org.junit.jupiter.api.Test;

import java.time.Duration;
import java.time.Instant;

import static org.junit.jupiter.api.Assertions.assertEquals;

class WindowsTest {
    @Test
    void alignsToWindowStart() {
        Duration five = Duration.ofMinutes(5);
        assertEquals(Instant.parse("2026-10-24T12:05:00Z"), Windows.start(Instant.parse("2026-10-24T12:07:59Z"), five));
        assertEquals(Instant.parse("2026-10-24T12:05:00Z"), Windows.start(Instant.parse("2026-10-24T12:05:00Z"), five));
        assertEquals(Instant.parse("2026-10-24T12:00:00Z"), Windows.start(Instant.parse("2026-10-24T12:04:59.999Z"), five));
    }
}
