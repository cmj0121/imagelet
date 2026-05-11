package stock

// This file uses the standard Go _test.go export trick: identifiers defined
// here are visible to the external _test package (stock_test) but not to
// production code.

// FormatPriceForTest exposes formatPrice for the external test package.
// Not part of the public API.
var FormatPriceForTest = formatPrice

// TitleForTest exposes titleFor for the external test package.
// Not part of the public API.
var TitleForTest = titleFor

// FormatThousandsForTest exposes formatThousands for the external test package.
var FormatThousandsForTest = formatThousands

// FormatLargeNumberForTest exposes formatLargeNumber for the external test package.
var FormatLargeNumberForTest = formatLargeNumber

// RocYearMonthLabelForTest exposes rocYearMonthLabel for the external test package.
var RocYearMonthLabelForTest = rocYearMonthLabel
