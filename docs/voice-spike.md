# Voice and cloud link: does it fit? (Phase 0, 2026-11-15)

The cloud plan has the board stream its microphone to the cloud and play speech back, while it keeps mirroring the
iPhone. The question was internal RAM: about 23 KB is free while mirroring, and starting a mirroring session needs
one 8 KB block for the mirror task's stack. The boot yawn alone once broke mirroring (64af9b2).

Measured on the Waveshare ESP32-S3-Touch-AMOLED-2.16, firmware 0.2.5. The test builds are
`flash.sh --amoled216 --audio-spike [--mirror-psram]`, with the board's `spike` command (`audio_spike.cpp`). A
Python socket on a computer stood in for the cloud.

## What was run
- **Audio path:** I2S both ways at 16 kHz.
  - ES7210 microphones in (mics 1 and 2, 30 dB gain).
  - ES8311 DAC out, playing silence with the amplifier off.
  - A task reading 20 ms frames, with its stack in PSRAM.
- **Link out:** the board opens a TCP connection to the computer and streams the mic as 16-bit PCM (256 kbps).
- **Mirror stack in PSRAM (`--mirror-psram`):** the mirror task's 8 KB stack is allocated once in PSRAM and used
  again by each session.

## Results (internal RAM: free / largest block)

| State | Stock mirror stack | Mirror stack in PSRAM |
|---|---|---|
| Idle | 33.8 KB / 11.8 KB | 33.1 KB / 11.8 KB |
| Mirroring | 22.9 KB / 7.7 KB | 29.7 KB / 11.8 KB |
| + audio path | 16.1 KB (cost 6.7 KB) | 26.5 KB (cost 4.6 KB: smaller DMA 3×80, I2C driver released) |
| + mic stream on a 2nd TCP socket | 13.9 KB | 25.7 KB, falling to ~13 KB while the computer's link caught up on video |

- **Stock mirror stack, audio on:** the computer's own Wi-Fi link to the board kept dropping back to BLE. This
  happened even with the stream off: the 6.7 KB the audio path took left too little for the Wi-Fi link's buffers.
  With audio off, the link held again.
- **Mirror stack in PSRAM, audio and stream on:** the link held for a minute and more.
  - Mirroring was **restarted with the audio path running** and came back.
  - The mic recording on the computer was clean: 103 s, RMS 220, peak 4165.
- **The second TCP socket costs up to ~5 KB of internal RAM** while its send buffer fills. With it, the computer's
  link stalled and screenshots stopped until the stream was closed.

## Decisions
1. **The mirror task's stack goes to PSRAM in the product.** It gives about 7 KB back during mirroring and removes
   the 8 KB block that starting a session needs. This is the change that makes voice fit. Before it becomes the
   default it needs a soak test:
   - long mirroring;
   - several session restarts;
   - an OTA update while mirroring. A task with its stack in PSRAM must not write flash itself; the mirror task
     doesn't.
2. **One link to the cloud, not two.** Audio goes as frames on the session link that already carries video and
   control (a new DATA type, like `0x12`). A second socket costs a send buffer of internal RAM and fights the first
   one for it.
3. **Compress the mic before it leaves**, for example IMA-ADPCM 4:1 (64 kbps). That means less in flight, smaller
   send buffers, and better behaviour on a weak home upload.
4. **Keep audio DMA small and the I2C driver closed** after setting the codecs up, as the spike did.
5. **Tap-to-talk first.** Open the audio path when someone taps the face and close it after the reply. A wake word
   that is always on would keep these 4.6 KB taken all the time.

## Not measured yet
- Playing real speech through the amplifier while the mic records (echo).
- Compression's CPU cost.
- A link to a real server over the internet; RTT and loss differ from the home network.
