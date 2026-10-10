# testdata

Samples files come from [here](http://techslides.com/sample-files-for-development)

To write tags to files you can use `lltag`:

```sh
lltag sample.* \
  -a "Test Artist" \
  -t "Test Title" \
  -A "Test Album" \
  -n "3" \
  -g "Jazz" \
  -d "2000" \
  -c "Test Comment" \
  --tag ALBUMARTIST="Test AlbumArtist" \
  --tag COMPOSER="Test Composer"\
  --tag DISCNUMBER="02" \
  --tag TRACKTOTAL="06"
```

`with_tags/sample.id3v22.mp3` is not from upstream, whose file of that name was a copy of the ID3v2.3 sample. It is the audio of `sample.id3v23.mp3` behind an ID3v2.2 tag built by hand to the specification, in the form old iTunes versions wrote: the same values in three-letter frames, a `PIC` frame, and MusicBrainz IDs in `UFI` and `TXX` frames. `id3v22SampleTag` in `tag_test.go` builds the tag, and `TestID3v22Sample` checks that the file is what it builds.
