package presets

// Merge applies the overlay over builtins. Overlay presets with a matching id
// override in place; others are appended. Disabled builtins are dropped.
// Origin is always recomputed here — the overlay file's value is ignored.
func Merge(builtins []Preset, overlay *Overlay) []Preset {
	disabled := map[string]bool{}
	overrides := map[string]Preset{}
	builtinIDs := map[string]bool{}
	for _, b := range builtins {
		builtinIDs[b.ID] = true
	}

	var customs []Preset
	if overlay != nil {
		for _, id := range overlay.DisabledBuiltins {
			disabled[id] = true
		}
		for _, p := range overlay.Presets {
			p.Origin = OriginUser
			if builtinIDs[p.ID] {
				overrides[p.ID] = p
			} else {
				customs = append(customs, p)
			}
		}
	}

	out := make([]Preset, 0, len(builtins)+len(customs))
	for _, b := range builtins {
		if disabled[b.ID] {
			continue
		}
		if ov, ok := overrides[b.ID]; ok {
			out = append(out, ov)
			continue
		}
		b.Origin = OriginBuiltin
		out = append(out, b)
	}
	return append(out, customs...)
}

// Catalog is the read facade over builtin defaults + the user overlay.
type Catalog struct {
	store *Store
}

func NewCatalog(store *Store) *Catalog {
	return &Catalog{store: store}
}

// List returns the merged catalog (builtins ⊕ overlay).
func (c *Catalog) List() ([]Preset, error) {
	builtins, err := LoadBuiltins()
	if err != nil {
		return nil, err
	}
	overlay, err := c.store.Load()
	if err != nil {
		return nil, err
	}
	return Merge(builtins, overlay), nil
}
