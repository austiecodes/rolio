use crate::Error;

/// A confidence value from 0.0 to 1.0, stored as a whole number of tenths.
#[derive(Clone, Copy, Debug, PartialEq, Eq, PartialOrd, Ord, Hash)]
pub struct Confidence(u8);

impl Confidence {
    pub const fn as_f32(self) -> f32 {
        self.0 as f32 / 10.0
    }
}

impl Default for Confidence {
    fn default() -> Self {
        Self(10)
    }
}

impl TryFrom<f32> for Confidence {
    type Error = Error;

    fn try_from(value: f32) -> Result<Self, Self::Error> {
        // Compare the allowed values directly. Do not round the input.
        for tenths in 0..=10 {
            if value == tenths as f32 / 10.0 {
                return Ok(Self(tenths));
            }
        }
        Err(Error::InvalidConfidence)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn accepts_each_decimal_tenth() {
        let values = [0.0, 0.1, 0.2, 0.3, 0.4, 0.5, 0.6, 0.7, 0.8, 0.9, 1.0];
        for value in values {
            let confidence = Confidence::try_from(value).unwrap();
            assert_eq!(confidence.as_f32().to_bits(), value.to_bits());
        }
    }

    #[test]
    fn defaults_to_full_confidence() {
        assert_eq!(Confidence::default(), Confidence::try_from(1.0).unwrap());
        assert_eq!(Confidence::default().as_f32(), 1.0);
    }

    #[test]
    fn accepts_negative_zero_as_zero() {
        let confidence = Confidence::try_from(-0.0).unwrap();
        assert_eq!(confidence, Confidence::try_from(0.0).unwrap());
        assert_eq!(confidence.as_f32().to_bits(), 0.0_f32.to_bits());
    }

    #[test]
    fn rejects_non_finite_values() {
        for value in [f32::NAN, f32::INFINITY, f32::NEG_INFINITY] {
            assert!(matches!(
                Confidence::try_from(value),
                Err(Error::InvalidConfidence)
            ));
        }
    }

    #[test]
    fn rejects_values_outside_the_range() {
        for value in [
            f32::MIN,
            -1.0,
            -0.1,
            -f32::MIN_POSITIVE,
            -f32::from_bits(1),
            1.000_000_1,
            1.1,
            f32::MAX,
        ] {
            assert!(matches!(
                Confidence::try_from(value),
                Err(Error::InvalidConfidence)
            ));
        }
    }

    #[test]
    fn rejects_extra_decimal_places() {
        for hundredths in 1..100 {
            if hundredths % 10 != 0 {
                let value = hundredths as f32 / 100.0;
                assert!(matches!(
                    Confidence::try_from(value),
                    Err(Error::InvalidConfidence)
                ));
            }
        }
        assert!(matches!(
            Confidence::try_from(0.333_333_34),
            Err(Error::InvalidConfidence)
        ));
    }

    #[test]
    fn rejects_adjacent_float_values_without_rounding() {
        for tenths in 0..=10 {
            let bits = (tenths as f32 / 10.0).to_bits();
            let next = f32::from_bits(bits + 1);
            assert!(matches!(
                Confidence::try_from(next),
                Err(Error::InvalidConfidence)
            ));
            if bits > 0 {
                let previous = f32::from_bits(bits - 1);
                assert!(matches!(
                    Confidence::try_from(previous),
                    Err(Error::InvalidConfidence)
                ));
            }
        }
    }

    #[test]
    fn orders_values_by_confidence() {
        let values: Vec<_> = (0..=10)
            .map(|tenths| Confidence::try_from(tenths as f32 / 10.0).unwrap())
            .collect();
        assert!(values.windows(2).all(|pair| pair[0] < pair[1]));
    }
}
