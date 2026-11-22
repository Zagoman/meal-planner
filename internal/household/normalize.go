package household

import "github.com/Zagoman/meal-planner/internal/shopping"

// NormalizeName is the normalization for pantry item names. It is
// shopping.Normalize, so a pantry entry always matches the ingredient keys
// the list builder compares it with ("Tomatoes (tinned)" -> "tomato").
func NormalizeName(name string) string { return shopping.Normalize(name) }
