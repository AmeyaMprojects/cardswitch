package dev.cardswitch.vault;

import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.ValueSource;

import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;

class LuhnTest {
    @ParameterizedTest
    @ValueSource(strings = {"4111111111111111", "5555555555554444", "378282246310005", "4012888888881881"})
    void acceptsTestCards(String pan) {
        assertTrue(Luhn.isValid(pan));
    }

    @ParameterizedTest
    @ValueSource(strings = {"4111111111111112", "1234", "41111111111111111111", "4111-1111-1111-1111", ""})
    void rejectsBadNumbers(String pan) {
        assertFalse(Luhn.isValid(pan));
    }
}
