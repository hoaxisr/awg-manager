package ndms

// ASCParams holds base AWG obfuscation parameters (firmware < 5.1 Alpha 3).
type ASCParams struct {
	Jc   int    `json:"jc"`
	Jmin int    `json:"jmin"`
	Jmax int    `json:"jmax"`
	S1   int    `json:"s1"`
	S2   int    `json:"s2"`
	H1   string `json:"h1"`
	H2   string `json:"h2"`
	H3   string `json:"h3"`
	H4   string `json:"h4"`
}

// ASCParamsExtended holds all AWG obfuscation parameters (firmware >= 5.1 Alpha 3).
type ASCParamsExtended struct {
	ASCParams
	S3 int    `json:"s3"`
	S4 int    `json:"s4"`
	I1 string `json:"i1"`
	I2 string `json:"i2"`
	I3 string `json:"i3"`
	I4 string `json:"i4"`
	I5 string `json:"i5"`
}
