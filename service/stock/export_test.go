package stock

// This file uses the standard Go _test.go export trick: identifiers defined
// here are visible to the external _test package (stock_test) but not to
// production code.

// FormatPriceForTest exposes formatPrice for the external test package.
// Not part of the public API.
var FormatPriceForTest = formatPrice

// IndexNameForTest exposes indexNameFor for the external test package.
// Not part of the public API.
var IndexNameForTest = indexNameFor
