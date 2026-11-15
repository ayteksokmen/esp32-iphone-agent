// See boot_sound.h. Pins and codec setup follow Waveshare's 07_ES8311 example for this board
// (github.com/waveshareteam/ESP32-S3-Touch-AMOLED-2.16); the codec driver is Espressif's (src/es8311).
#include "boot_sound.h"

#if BOARD_AMOLED_216
#include <Arduino.h>
#include <Wire.h>
#include <ESP_I2S.h>
#include <math.h>
#include "logbuf.h"
#include "src/es8311/es8311.h"

namespace {
const int IIC_SDA = 15, IIC_SCL = 14;
const int I2S_MCK = 42, I2S_BCK = 9, I2S_WS = 45, I2S_DO = 8, I2S_DI = 10;
const int AMP_ON = 46;      // the speaker amplifier's enable pin
const int RATE = 16000;
const int VOLUME = 72;      // codec volume, 0-100
const int CHUNK = 256;      // frames synthesized and written at a time

float frand() { return (esp_random() & 0xffff) / 32768.f - 1; }

// a soft peak around f0 of width bw: how loud a vowel's resonance makes a harmonic at f
float formant(float f, float f0, float bw) {
  float d = (f - f0) / bw;
  return expf(-d * d);
}

float lerp(float a, float b, float t) { return a + (b - a) * fminf(fmaxf(t, 0), 1); }

// The yawn, in seconds: a breath in, a voiced "aaah-oh" that rises and falls, a breath out; then two quick
// rising chirps, the robot awake. The face's timeline (status_screen.cpp, WAKE_*) follows the same times.
const float YAWN_START = 0.25f, YAWN_PEAK = 0.6f, YAWN_END = 1.35f, CHIRP_AT = 1.65f, TOTAL = 2.05f;

float sample(float t, float &phase, float &breathLp) {
  float out = 0;
  // breath: filtered noise, in before the yawn, out after it
  float breath = 0;
  if (t < YAWN_START) breath = 0.10f * sinf((float)M_PI * t / YAWN_START);
  else if (t > YAWN_END - 0.1f && t < CHIRP_AT - 0.05f)
    breath = 0.07f * sinf((float)M_PI * (t - (YAWN_END - 0.1f)) / (CHIRP_AT - 0.05f - (YAWN_END - 0.1f)));
  breathLp += 0.25f * (frand() - breathLp);
  out += breath * breathLp * 3;

  // voice: harmonics of a gliding pitch, shaped by two formants moving from "a" to "o"
  if (t >= YAWN_START && t < YAWN_END) {
    float u = (t - YAWN_START) / (YAWN_END - YAWN_START);  // 0..1 through the yawn
    float f0 = t < YAWN_PEAK ? lerp(240, 330, (t - YAWN_START) / (YAWN_PEAK - YAWN_START))
                             : lerp(330, 165, (t - YAWN_PEAK) / (YAWN_END - YAWN_PEAK));
    f0 *= 1 + 0.018f * sinf(2 * (float)M_PI * 5.5f * t);  // a little vibrato
    float f1 = lerp(820, 480, u), f2 = lerp(1250, 850, u);
    float env = fminf(u / 0.12f, 1) * fminf((1 - u) / 0.35f, 1);
    phase += 2 * (float)M_PI * f0 / RATE;
    if (phase > 2 * (float)M_PI * 64) phase -= 2 * (float)M_PI * 64;
    float v = 0;
    for (int k = 1; k <= 10; k++) {
      float f = k * f0;
      float g = 0.25f / k + formant(f, f1, 220) + 0.6f * formant(f, f2, 260);
      v += g * sinf(k * phase);
    }
    out += env * 0.16f * v + env * 0.03f * breathLp;
  }

  // chirps: two short sine sweeps upwards
  for (int i = 0; i < 2; i++) {
    float c0 = CHIRP_AT + i * 0.14f, d = 0.09f;
    if (t >= c0 && t < c0 + d) {
      float u = (t - c0) / d;
      float f = lerp(900 + i * 250, 1500 + i * 350, u);
      static float cphase = 0;
      cphase += 2 * (float)M_PI * f / RATE;
      out += 0.35f * sinf((float)M_PI * u) * sinf(cphase);
    }
  }
  return out;
}

volatile bool busy = false;

void play(void *) {
  I2SClass i2s;
  i2s.setPins(I2S_BCK, I2S_WS, I2S_DO, I2S_DI, I2S_MCK);
  bool ok = i2s.begin(I2S_MODE_STD, RATE, I2S_DATA_BIT_WIDTH_16BIT, I2S_SLOT_MODE_STEREO, I2S_STD_SLOT_BOTH);
  es8311_handle_t es = nullptr;
  if (ok) {
    Wire.begin(IIC_SDA, IIC_SCL);
    es = es8311_create(0, ES8311_ADDRRES_0);
    const es8311_clock_config_t clk = {false, false, true, RATE * 256, RATE};
    ok = es && es8311_init(es, &clk, ES8311_RESOLUTION_16, ES8311_RESOLUTION_16) == ESP_OK &&
         es8311_sample_frequency_config(es, clk.mclk_frequency, clk.sample_frequency) == ESP_OK &&
         es8311_voice_volume_set(es, VOLUME, NULL) == ESP_OK;
  }
  if (ok) {
    pinMode(AMP_ON, OUTPUT);
    digitalWrite(AMP_ON, HIGH);
    static int16_t buf[CHUNK * 2];
    float phase = 0, breathLp = 0;
    int total = (int)(TOTAL * RATE);
    for (int n = 0; n < total; n += CHUNK) {
      for (int i = 0; i < CHUNK; i++) {
        float s = fmaxf(fminf(sample((n + i) / (float)RATE, phase, breathLp), 1), -1);
        buf[2 * i] = buf[2 * i + 1] = (int16_t)(s * 22000);
      }
      i2s.write((uint8_t *)buf, sizeof buf);
    }
    memset(buf, 0, sizeof buf);  // let the last samples out before the amplifier goes off
    for (int i = 0; i < 4; i++) i2s.write((uint8_t *)buf, sizeof buf);
    digitalWrite(AMP_ON, LOW);
  } else {
    board_log("sound: speaker did not start");
  }
  if (es) {
    es8311_voice_mute(es, true);
    es8311_delete(es);
  }
  i2s.end();
  Wire.end();
  busy = false;
  vTaskDelete(NULL);
}
}  // namespace

void boot_sound_play() {
  // a task of its own, so the face animates meanwhile (boot waits for it: boot_sound_busy)
  busy = true;
  if (xTaskCreatePinnedToCore(play, "sound", 4096, NULL, 1, NULL, 1) != pdPASS) {
    busy = false;
    board_log("sound: no task");
  }
}

bool boot_sound_busy() { return busy; }

#else
void boot_sound_play() {}
bool boot_sound_busy() { return false; }
#endif
