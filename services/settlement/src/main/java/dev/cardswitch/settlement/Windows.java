package dev.cardswitch.settlement;

import java.time.Duration;
import java.time.Instant;

/** Fixed, aligned settlement windows: [start, start + size). */
public final class Windows {
    private Windows() {}

    public static Instant start(Instant at, Duration size) {
        long ms = size.toMillis();
        return Instant.ofEpochMilli(Math.floorDiv(at.toEpochMilli(), ms) * ms);
    }
}
