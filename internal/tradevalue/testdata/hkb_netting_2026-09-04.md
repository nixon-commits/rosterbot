# HKB two-sided control, captured 2026-09-04

Every row in `hkb_calculator_2026-09-04.csv` is one-sided, because HKB's
displayed per-side total is **not** that side's own package-adjusted value once
both sides are filled: it nets the two deductions against each other and charges
only the excess to whichever side is carrying more of it.

Captured live on the same day, same sitting:

```
https://harryknowsball.com/calculator
  ?teamOne=seQYv10w,QbdzhwOi
  &teamTwo=IhaFkRsx,j74ZhM3m,OaD91YmB
  &leagueSize=12
```

| side | assets (as displayed) | raw | own deduction | shown deduction | shown total |
|---|---|---|---|---|---|
| Team 1 | Ohtani 10000, Witt 9758 | 19758 | 3708 | none rendered | 19758 |
| Team 2 | Caminero 8556, Wood 7698, Burns 5026 | 21280 | 4935 | −1227 | 20053 |

`4935 − 3708 = 1227`, and Team 1's own 3708 is zeroed. So the netting subtracts
`min(dedA, dedB)` from both sides.

**This is why `Side.Adjusted()` does not model it.** The netting shifts both
sides by the same amount, so it cannot change which side leads, nor the absolute
gap between them: un-netted, `16344.36 − 16049.96 = 294.4`; netted and floored,
`20053 − 19758 = 295`. It is a presentation choice about which side gets the
red number, not a repricing — and `Side` is a per-side type, so a function of
the opposing side has no signature to live in here. `Evaluate` compares
un-netted per-side values and reaches the same leader HKB shows.
