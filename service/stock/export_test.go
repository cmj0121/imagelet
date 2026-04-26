package stock

// This file uses the standard Go _test.go export trick: identifiers defined
// here are visible to the external _test package (stock_test) but not to
// production code. Lets us keep stripPylonSyntax / formatPrice unexported
// while still unit-testing them directly.

// StripPylonSyntaxForTest exposes stripPylonSyntax for the external test
// package. Not part of the public API.
var StripPylonSyntaxForTest = stripPylonSyntax

// FormatPriceForTest exposes formatPrice for the external test package.
// Not part of the public API.
var FormatPriceForTest = formatPrice
