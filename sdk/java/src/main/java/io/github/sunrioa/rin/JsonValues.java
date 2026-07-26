package io.github.sunrioa.rin;

import java.math.BigDecimal;
import java.util.List;
import java.util.Map;
import java.util.Objects;

/** JSON-semantic comparisons for values crossing codecs or persistence layers. */
public final class JsonValues {
    private JsonValues() { }

    public static boolean equivalent(Object left, Object right) {
        if (left instanceof Number leftNumber && right instanceof Number rightNumber) {
            try {
                return new BigDecimal(leftNumber.toString())
                        .compareTo(new BigDecimal(rightNumber.toString())) == 0;
            } catch (NumberFormatException invalid) {
                return false;
            }
        }
        if (left instanceof Map<?, ?> leftMap && right instanceof Map<?, ?> rightMap) {
            if (!leftMap.keySet().equals(rightMap.keySet())) return false;
            for (Object key : leftMap.keySet()) {
                if (!equivalent(leftMap.get(key), rightMap.get(key))) return false;
            }
            return true;
        }
        if (left instanceof List<?> leftList && right instanceof List<?> rightList) {
            if (leftList.size() != rightList.size()) return false;
            for (int index = 0; index < leftList.size(); index++) {
                if (!equivalent(leftList.get(index), rightList.get(index))) return false;
            }
            return true;
        }
        return Objects.equals(left, right);
    }
}
