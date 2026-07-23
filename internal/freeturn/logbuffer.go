package freeturn

// processLogMaxLines — сколько последних строк stdout/stderr держим для UI.
// При -n=10 и captcha-лавине 80 строк исчезали за секунды.
const processLogMaxLines = 500
