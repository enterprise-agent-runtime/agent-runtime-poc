package execproto

import "encoding/json"

// A Go nil slice encodes as null, but A06 requires an array for these
// properties. Each MarshalJSON below writes [] instead; the plain types
// drop the method so the conversion does not recurse.

type (
	helloResultPlain  HelloResult
	listResultPlain   ListResult
	searchResultPlain SearchResult
	patchResultPlain  PatchResult
)

// MarshalJSON writes a nil Roots as [].
func (r HelloResult) MarshalJSON() ([]byte, error) {
	if r.Roots == nil {
		r.Roots = []HelloRoot{}
	}
	return json.Marshal(helloResultPlain(r))
}

// MarshalJSON writes a nil Entries as [].
func (r ListResult) MarshalJSON() ([]byte, error) {
	if r.Entries == nil {
		r.Entries = []ListEntry{}
	}
	return json.Marshal(listResultPlain(r))
}

// MarshalJSON writes a nil Matches as [].
func (r SearchResult) MarshalJSON() ([]byte, error) {
	if r.Matches == nil {
		r.Matches = []SearchMatch{}
	}
	return json.Marshal(searchResultPlain(r))
}

// MarshalJSON writes a nil Files as [].
func (r PatchResult) MarshalJSON() ([]byte, error) {
	if r.Files == nil {
		r.Files = []PatchFile{}
	}
	return json.Marshal(patchResultPlain(r))
}
