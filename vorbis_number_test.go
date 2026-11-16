package tag

import "testing"

func TestVorbisTrackDiscNumberForms(t *testing.T) {
	for _, tc := range []struct {
		name                       string
		fields                     map[string]string
		track, tracks, disc, discs int
	}{
		{"combined", map[string]string{"tracknumber": "02/12", "discnumber": "1/2"}, 2, 12, 1, 2},
		{"separate", map[string]string{"tracknumber": "02", "tracktotal": "12", "discnumber": "1", "disctotal": "2"}, 2, 12, 1, 2},
		{"aliases", map[string]string{"tracknumber": "2", "totaltracks": "12", "discnumber": "1", "totaldiscs": "2"}, 2, 12, 1, 2},
		{"override", map[string]string{"tracknumber": "2/9", "tracktotal": "12", "discnumber": "1/9", "disctotal": "2"}, 2, 12, 1, 2},
		{"unknown", map[string]string{}, 0, 0, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &metadataVorbis{c: tc.fields}
			if n, total := m.Track(); n != tc.track || total != tc.tracks {
				t.Errorf("track: %d/%d", n, total)
			}
			if n, total := m.Disc(); n != tc.disc || total != tc.discs {
				t.Errorf("disc: %d/%d", n, total)
			}
		})
	}
}
