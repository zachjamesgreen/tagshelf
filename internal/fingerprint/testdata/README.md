# Test audio for the fingerprint module

Every file here is the same twelve seconds of synthetic sound: a tune of sine tones made by a formula, in no way a recording of anything. They were made with ffmpeg 7.1.1 from

```
aevalsrc=0.5*sin(2*PI*t*(220*pow(2\,mod(floor(t*2)\,16)/12)))+0.25*sin(2*PI*t*(330*pow(2\,mod(floor(t*3)\,11)/12)))*(0.6+0.4*sin(2*PI*t/3)):s=22050:d=12
```

with `-map_metadata -1 -fflags +bitexact -flags:a +bitexact` and, for each file:

| File | Encoder and options |
|---|---|
| `tune.flac` | `-c:a flac -compression_level 12 -ar 8000` |
| `tune-alac.m4a` | `-c:a alac -ar 8000` |
| `tune.wav` | `-c:a pcm_s16le -ar 8000` |
| `tune.mp3` | `-c:a libmp3lame -b:a 32k -write_xing 0 -id3v2_version 0` |
| `tune.m4a` | `-c:a aac -b:a 32k` |
| `tune.ogg` | `-c:a vorbis -strict -2 -ac 2 -b:a 48k` |
| `tune.opus` | `-c:a opus -strict -2 -ac 2 -b:a 32k -ar 48000` |
| `tune.wma` | `-c:a wmav2 -b:a 32k` |

The fingerprints the tests expect are what the module gave for these files when they were made, compared then with AcoustID's `fpcalc` 1.5.1.
