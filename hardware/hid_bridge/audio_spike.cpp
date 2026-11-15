// Phase 0 of the cloud and voice plan: does a voice pipeline fit in internal RAM next to mirroring? A test build
// (flash.sh --audio-spike) runs the whole audio path the product would: the I2S bus both ways (ES7210 mics in,
// ES8311 DAC out, playing silence with the amplifier off), a task reading 20 ms frames, and optionally a TCP link
// out to a computer streaming the mic as 16 kHz PCM (as the board would to the cloud).
//
//   spike                       memory and audio counters
//   spike audio on [psram|internal] [desc] [frames]   start; task stack where, DMA buffers (default psram 3 160)
//   spike audio off
//   spike net HOST PORT | off   stream the mic there
#include "audio_spike.h"

#if AUDIO_SPIKE && BOARD_AMOLED_216
#include <Wire.h>
#include <math.h>
#include "driver/i2s_std.h"
#include "esp_heap_caps.h"
#include "freertos/idf_additions.h"
#include "lwip/sockets.h"
#include "src/es7210/es7210.h"
#include "src/es8311/es8311.h"

namespace {
const int RATE = 16000;
i2s_chan_handle_t tx, rx;
TaskHandle_t task;
volatile bool run = false, parked = true;
volatile int sock = -1;
volatile uint32_t frames, level, sentB, errs, maxLoopUs;
bool stackPsram = true;
int16_t *in, *mono, *zero;  // PSRAM
int frameN = 320;           // 20 ms
size_t heapBefore, bigBefore;

void loopTask(void *) {
  for (;;) {
    if (!run) {
      parked = true;
      ulTaskNotifyTake(pdTRUE, portMAX_DELAY);
      continue;
    }
    parked = false;
    size_t n = 0;
    uint32_t t0 = micros();
    if (i2s_channel_read(rx, in, frameN * 4, &n, 200) != ESP_OK || !n) {
      errs++;
      continue;
    }
    int k = n / 4;
    uint64_t acc = 0;
    for (int i = 0; i < k; i++) {
      mono[i] = in[2 * i];
      acc += (int32_t)mono[i] * mono[i];
    }
    level = (uint32_t)sqrt((double)acc / k);
    frames += k;
    size_t w;
    i2s_channel_write(tx, zero, n, &w, 200);
    int s = sock;
    if (s >= 0) {
      int r = send(s, mono, k * 2, MSG_DONTWAIT);
      if (r > 0) sentB += r;
      else if (errno != EAGAIN && errno != EWOULDBLOCK) errs++;
    }
    uint32_t dt = micros() - t0;
    if (dt > maxLoopUs) maxLoopUs = dt;
  }
}

String mem() {
  char b[220];
  snprintf(b, sizeof b, "\"heap\":%u,\"big\":%u,\"min\":%u,\"psram\":%u",
           (unsigned)heap_caps_get_free_size(MALLOC_CAP_INTERNAL),
           (unsigned)heap_caps_get_largest_free_block(MALLOC_CAP_INTERNAL),
           (unsigned)heap_caps_get_minimum_free_size(MALLOC_CAP_INTERNAL),
           (unsigned)heap_caps_get_free_size(MALLOC_CAP_SPIRAM));
  return b;
}

String audioOn(bool psram, int desc, int fr) {
  if (tx) return "err already on";
  heapBefore = heap_caps_get_free_size(MALLOC_CAP_INTERNAL);
  bigBefore = heap_caps_get_largest_free_block(MALLOC_CAP_INTERNAL);
  if (!in) {
    in = (int16_t *)heap_caps_malloc(640 * 4, MALLOC_CAP_SPIRAM);
    mono = (int16_t *)heap_caps_malloc(640 * 2, MALLOC_CAP_SPIRAM);
    zero = (int16_t *)heap_caps_calloc(640 * 2, 2, MALLOC_CAP_SPIRAM);
  }
  frameN = 320;
  i2s_chan_config_t cc = I2S_CHANNEL_DEFAULT_CONFIG(I2S_NUM_0, I2S_ROLE_MASTER);
  cc.dma_desc_num = desc;
  cc.dma_frame_num = fr;
  cc.auto_clear = true;
  if (i2s_new_channel(&cc, &tx, &rx) != ESP_OK) return "err i2s channel";
  i2s_std_config_t sc = {
      .clk_cfg = I2S_STD_CLK_DEFAULT_CONFIG(RATE),
      .slot_cfg = I2S_STD_PHILIPS_SLOT_DEFAULT_CONFIG(I2S_DATA_BIT_WIDTH_16BIT, I2S_SLOT_MODE_STEREO),
      .gpio_cfg = {.mclk = GPIO_NUM_42, .bclk = GPIO_NUM_9, .ws = GPIO_NUM_45, .dout = GPIO_NUM_8, .din = GPIO_NUM_10,
                   .invert_flags = {}},
  };
  sc.clk_cfg.mclk_multiple = I2S_MCLK_MULTIPLE_256;
  if (i2s_channel_init_std_mode(tx, &sc) != ESP_OK || i2s_channel_init_std_mode(rx, &sc) != ESP_OK)
    return "err i2s std";
  i2s_channel_enable(tx);
  i2s_channel_enable(rx);
  Wire.begin(15, 14);
  es8311_handle_t es = es8311_create(0, ES8311_ADDRRES_0);
  const es8311_clock_config_t clk = {false, false, true, RATE * 256, RATE};
  bool dac = es && es8311_init(es, &clk, ES8311_RESOLUTION_16, ES8311_RESOLUTION_16) == ESP_OK &&
             es8311_sample_frequency_config(es, clk.mclk_frequency, clk.sample_frequency) == ESP_OK;
  if (es) es8311_delete(es);
  audio_hal_codec_config_t cfg = {};
  cfg.adc_input = AUDIO_HAL_ADC_INPUT_ALL;
  cfg.codec_mode = AUDIO_HAL_CODEC_MODE_ENCODE;
  cfg.i2s_iface.mode = AUDIO_HAL_MODE_SLAVE;
  cfg.i2s_iface.fmt = AUDIO_HAL_I2S_NORMAL;
  cfg.i2s_iface.samples = AUDIO_HAL_16K_SAMPLES;
  cfg.i2s_iface.bits = AUDIO_HAL_BIT_LENGTH_16BITS;
  esp_err_t e = es7210_adc_init(&Wire, &cfg);
  e |= es7210_adc_config_i2s(cfg.codec_mode, &cfg.i2s_iface);
  e |= es7210_adc_set_gain((es7210_input_mics_t)(ES7210_INPUT_MIC1 | ES7210_INPUT_MIC2), (es7210_gain_value_t)GAIN_30DB);
  e |= es7210_adc_ctrl_state(cfg.codec_mode, AUDIO_HAL_CTRL_START);
  Wire.end();  // I2C is only needed to set the codecs up: give its driver's RAM back
  if (!task) {
    stackPsram = psram;
    BaseType_t ok = psram ? xTaskCreatePinnedToCoreWithCaps(loopTask, "mic", 4096, NULL, 5, &task, 1, MALLOC_CAP_SPIRAM)
                          : xTaskCreatePinnedToCore(loopTask, "mic", 4096, NULL, 5, &task, 1);
    if (ok != pdPASS) return "err task";
  }
  frames = level = sentB = errs = maxLoopUs = 0;
  run = true;
  xTaskNotifyGive(task);
  return String("ok dac ") + (dac ? 1 : 0) + " adc " + (e == ESP_OK ? 1 : 0);
}

String audioOff() {
  if (!tx) return "ok";
  run = false;
  for (int i = 0; i < 50 && !parked; i++) delay(10);
  i2s_channel_disable(tx);
  i2s_channel_disable(rx);
  i2s_del_channel(tx);
  i2s_del_channel(rx);
  tx = rx = nullptr;
  Wire.begin(15, 14);
  es7210_adc_ctrl_state(AUDIO_HAL_CODEC_MODE_ENCODE, AUDIO_HAL_CTRL_STOP);
  Wire.end();
  return "ok";
}

String netTo(const String &host, int port) {
  if (sock >= 0) {
    int s = sock;
    sock = -1;
    close(s);
  }
  if (host == "off") return "ok";
  int s = socket(AF_INET, SOCK_STREAM, 0);
  if (s < 0) return "err socket";
  sockaddr_in a = {};
  a.sin_family = AF_INET;
  a.sin_port = htons(port);
  inet_pton(AF_INET, host.c_str(), &a.sin_addr);
  timeval tv = {3, 0};
  setsockopt(s, SOL_SOCKET, SO_SNDTIMEO, &tv, sizeof tv);
  if (connect(s, (sockaddr *)&a, sizeof a) != 0) {
    close(s);
    return "err connect";
  }
  sock = s;
  return "ok";
}
}  // namespace

String audio_spike(const String &arg) {
  char a[16] = "", b[40] = "", c[16] = "";
  int n3 = 0;
  sscanf(arg.c_str(), "%15s %39s %15s %d", a, b, c, &n3);
  if (!strcmp(a, "audio") && !strcmp(b, "on")) {
    int desc = 3, fr = 160;
    sscanf(arg.c_str(), "%*s %*s %*s %d %d", &desc, &fr);
    String r = audioOn(strcmp(c, "internal") != 0, desc, fr);
    return r + " took " + (int)(heapBefore - heap_caps_get_free_size(MALLOC_CAP_INTERNAL)) + " {" + mem() + "}";
  }
  if (!strcmp(a, "audio") && !strcmp(b, "off")) return audioOff() + " {" + mem() + "}";
  if (!strcmp(a, "net")) {
    size_t h0 = heap_caps_get_free_size(MALLOC_CAP_INTERNAL);
    String r = netTo(b, atoi(c));
    return r + " took " + (int)(h0 - heap_caps_get_free_size(MALLOC_CAP_INTERNAL)) + " {" + mem() + "}";
  }
  char s[200];
  snprintf(s, sizeof s, ",\"audio\":%d,\"stack\":\"%s\",\"frames\":%lu,\"lvl\":%lu,\"sentKB\":%lu,\"errs\":%lu,\"maxloop_us\":%lu,\"net\":%d}",
           tx ? 1 : 0, stackPsram ? "psram" : "internal", (unsigned long)frames, (unsigned long)level,
           (unsigned long)(sentB / 1024), (unsigned long)errs, (unsigned long)maxLoopUs, sock >= 0);
  return "ok {" + mem() + s;
}

#else
String audio_spike(const String &) { return "err not a --audio-spike build"; }
#endif
