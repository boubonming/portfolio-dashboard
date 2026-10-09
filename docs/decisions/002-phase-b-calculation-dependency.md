# Phase B calculation dependency decision

The implementation was required to evaluate `github.com/shopspring/decimal`, as proposed by the plan. The package was not added because the environment's dependency threat-intelligence gate could not complete OSV and deps.dev verification before the package install deadline. Adding it without a completed security check would violate the repository's dependency review requirement.

This slice therefore uses only Go's standard-library `math/big` (`big.Rat`) behind `internal/domain.Decimal`. Decimal literals are parsed directly, arithmetic is exact for finite decimal inputs, and FX division uses an explicit 18-decimal half-away-from-zero rule. No `float64` conversion occurs. The decision can be revisited in a later dependency review if the package's security and license checks complete successfully.
