package fingerprint

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// prints are the fingerprints of the synthetic test audio in testdata (see
// its README). fpcalc says whether AcoustID's own tool, fpcalc 1.5.1, gives
// the very same text for the file. It does for all but two, whose decoders
// FFmpeg has changed since the FFmpeg that fpcalc was built with: there the
// two fingerprints differ in a few bits of a hundred, which AcoustID takes
// for the same audio.
var prints = []struct {
	file        string
	duration    int
	fingerprint string
	fpcalc      bool
}{
	{"tune-alac.m4a", 12, "AQAAS8qlhVpwr0J_mOkF5sejB9eJ7_iRv0im0WB73EKf48OPR3iPZ0jeBHmOHf8zPGj-o4_wI8ePH5-xFz943LKCSfPRIHhw4Mev4CQ-_B-SPAj1g8txWeidGNcGHOE_ULokQuu6CMfR_vCHE86POH44_Dh8AcdNXA94HO8hAEAUIdIooICDSDAFGQWcGdMAAUw6JAgSRFjCCdHUCIIAJwIhLiBgIBEgFPAMMbQIcdAAoJUAnLAtOgOqOwU", true},
	{"tune.flac", 12, "AQAAS8qlhVpwr0J_mOkF5sejB9eJ7_iRv0im0WB73EKf48OPR3iPZ0jeBHmOHf8zPGj-o4_wI8ePH5-xFz943LKCSfPRIHhw4Mev4CQ-_B-SPAj1g8txWeidGNcGHOE_ULokQuu6CMfR_vCHE86POH44_Dh8AcdNXA94HO8hAEAUIdIooICDSDAFGQWcGdMAAUw6JAgSRFjCCdHUCIIAJwIhLiBgIBEgFPAMMbQIcdAAoJUAnLAtOgOqOwU", true},
	{"tune.m4a", 12, "AQAATMqlhVpwr8IPJkwv9D-uBz-u_fiRv0im0WB73OiT48PxCD-eIXkT5Mc-_M_woPmPPsKPPDh-fMae4uCP-womzYcR4sGBH7-Ck_iI_IfyINQPLsf1oH9wbUDQ84MknQn6DS-O4oMP54jTPBz-4_AFHDdxPeDx4-kh_ABAKCGAGAYcUAIgwRRkEnBwLiDASYQEQYIIS7hg1QhhmSBCIMQFBAwcAoQCjAEEmCCEOcisAkwQtkVnQDXBlMEA", true},
	{"tune.mp3", 12, "AQAATMqlhRrutegf-AfTHL-OJ8d1HsfXF5pG_Aefok-O48Ef4T2OnEHyYN9xPP9Q72ieGA_yHD-O_9hT_CAuK5ikw_QR4niAH8fFBf9wRM-H5D-YHTeeC33U48MR_qCS04KmfQkeDhd6fD98EkeqN8Kf4_Bx4BtxHTz-40jeIwdAlDBEKiIYUABQBAgiRAIOGmOAEYacSsAQSoYmmgpiBAJQCoS48JAZLSARwEMmBIFoEUIYEwYAg5kDgm3VmbBMIKYUAQ", true},
	{"tune.ogg", 12, "AQAAS8qlhVpwr0J_mOkF5sejB9eJ7_iRv0im0WB73EKf48OPR3iPZ0jeBHmOHf8zPGj-o4_wI8ePH5-xFz94XJYVTPXRIHhw4Mev4CQ-_B-SPAj1g8txWeidGNcGHOE_ULokQuu6CMfR_vCHE86POH44_Dh8AcdNXA94_Hh6CEAUIdIooICDSDAFGQWcGdMAAUw6JAgSRFjCCdHUCIIABwIhLiBgIBEgFPAMMbQIcdAAoJUAnLAtOgOqCaYA", false},
	{"tune.opus", 12, "AQAAS8qlhVpwr0J_mOkF5sejB9eJ7_iRv0im0WB73EKf48OPR3iPZ0jeBHmOHf8zPGj-o4_wI8ePH5-xFz94XJYVTPXRIHhw4Mev4CQ-_EeSB6F-cMFloXeCaztwhP9A6ZIIresiHEf7wx9OOD_i-OHwwx8OHDdxPeBxvIcAQBQh0iiggINIMAUZBZwZ0wABTDokCBJEWMIJ0dQIggAHAiEuIGAgESAUYIgRtAhx0ACglQCcaDU6A6o7BQ", true},
	{"tune.wav", 12, "AQAAS8qlhVpwr0J_mOkF5sejB9eJ7_iRv0im0WB73EKf48OPR3iPZ0jeBHmOHf8zPGj-o4_wI8ePH5-xFz943LKCSfPRIHhw4Mev4CQ-_B-SPAj1g8txWeidGNcGHOE_ULokQuu6CMfR_vCHE86POH44_Dh8AcdNXA94HO8hAEAUIdIooICDSDAFGQWcGdMAAUw6JAgSRFjCCdHUCIIAJwIhLiBgIBEgFPAMMbQIcdAAoJUAnLAtOgOqOwU", true},
	{"tune.wma", 12, "AQAAS4ojrcmC60EPJkyP_3jy4Ofx4euLZEe8g2V69HhyHP8iHO-Rh0FyBz_24_9QR_ATo1eQHw9-_FiHpwFxKxjlo6GPIscD4DnuBedx5P-Q5DkY5fh9PE_QRz--4SjiR5CS5ErwdcODC-1--CTg5GqE-D-ODz7w4Tp4HT9-5D0SACg0UsQwoAhwCBBjJAeiGcaAAI46hIShVnBCFTWIGmEQB5ARcAgQwAJBmBAAQJSRYQwAg4AQ3HzRBQEMM8EU", false},
}

// The module gives each format the fingerprint recorded for it, on every
// system: the same code runs everywhere.
func TestOf(t *testing.T) {
	for _, tt := range prints {
		t.Run(tt.file, func(t *testing.T) {
			got, err := Of(context.Background(), filepath.Join("testdata", tt.file))
			if err != nil {
				t.Fatal(err)
			}
			if got.Duration != tt.duration {
				t.Errorf("Duration = %d, want %d", got.Duration, tt.duration)
			}
			if got.Fingerprint != tt.fingerprint {
				t.Errorf("Fingerprint =\n%s\nwant\n%s", got.Fingerprint, tt.fingerprint)
			}
		})
	}
}

// Every format the audio is in gives the tune one fingerprint, or nearly:
// the lossless ones the very same.
func TestLosslessFormatsAgree(t *testing.T) {
	by := map[string]string{}
	for _, tt := range prints {
		by[tt.file] = tt.fingerprint
	}
	if by["tune.flac"] != by["tune.wav"] || by["tune.flac"] != by["tune-alac.m4a"] {
		t.Error("FLAC, WAV and ALAC of the same audio have different fingerprints")
	}
}

// Many files at once, as a release match asks for.
func TestOfAtOnce(t *testing.T) {
	var wg sync.WaitGroup
	for range 3 {
		for _, tt := range prints {
			wg.Add(1)
			go func() {
				defer wg.Done()
				got, err := Of(context.Background(), filepath.Join("testdata", tt.file))
				if err != nil || got.Fingerprint != tt.fingerprint {
					t.Errorf("%s: %v, the fingerprint as recorded: %t", tt.file, err, got.Fingerprint == tt.fingerprint)
				}
			}()
		}
	}
	wg.Wait()
}

// What cannot be listened to says why, and names the file.
func TestOfRefusals(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	for name, path := range map[string]string{
		"a file that is not audio": write("notes.txt", strings.Repeat("just some words\n", 200)),
		"an empty file":            write("empty.mp3", ""),
	} {
		if got, err := Of(context.Background(), path); err == nil || !strings.Contains(err.Error(), filepath.Base(path)) {
			t.Errorf("%s: %+v, %v; want an error that names the file", name, got, err)
		}
	}
	if _, err := Of(context.Background(), filepath.Join(dir, "none.flac")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a file that is not there: %v", err)
	}
	if _, err := Of(context.Background(), dir); err == nil {
		t.Error("a folder was listened to")
	}

	// A name that is not plain ASCII, and has a space.
	data, err := os.ReadFile(filepath.Join("testdata", "tune.mp3"))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := Of(context.Background(), write("Sång 日本 01.mp3", string(data))); err != nil || got.Fingerprint == "" {
		t.Errorf("a file with a name outside ASCII: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Of(ctx, filepath.Join("testdata", "tune.flac")); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled: %v", err)
	}
}

func TestParse(t *testing.T) {
	got, err := parse("DURATION=187\nFINGERPRINT=AQAA\n", "a.mp3")
	if err != nil || got != (Print{Fingerprint: "AQAA", Duration: 187}) {
		t.Errorf("parse = %+v, %v", got, err)
	}
	for _, out := range []string{"", "DURATION=187\n", "DURATION=long\nFINGERPRINT=AQAA\n"} {
		if got, err := parse(out, "a.mp3"); err == nil {
			t.Errorf("parse(%q) = %+v, want an error", out, got)
		}
	}
}
