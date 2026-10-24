package dev.cardswitch.vault;

/** Luhn (mod 10) check used by every card network to catch typos in PANs. */
public final class Luhn {
    private Luhn() {}

    /** True if pan is 12-19 digits and passes the Luhn checksum. */
    public static boolean isValid(String pan) {
        if (pan == null || pan.length() < 12 || pan.length() > 19) {
            return false;
        }
        int sum = 0;
        boolean doubleIt = false;
        for (int i = pan.length() - 1; i >= 0; i--) {
            char c = pan.charAt(i);
            if (c < '0' || c > '9') {
                return false;
            }
            int d = c - '0';
            if (doubleIt) {
                d *= 2;
                if (d > 9) {
                    d -= 9;
                }
            }
            sum += d;
            doubleIt = !doubleIt;
        }
        return sum % 10 == 0;
    }
}
