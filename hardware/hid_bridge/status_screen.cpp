// See status_screen.h. Pins and panel init follow Waveshare's 01_HelloWorld example for this board
// (github.com/waveshareteam/ESP32-S3-Touch-AMOLED-2.16, GFX Library for Arduino 1.6.4).
#include "status_screen.h"

#if BOARD_AMOLED_216
#include <Arduino_GFX_Library.h>
#include <esp_heap_caps.h>
#include <math.h>

namespace {
const int LCD_CS = 12, LCD_SCLK = 38, LCD_D0 = 4, LCD_D1 = 5, LCD_D2 = 6, LCD_D3 = 7, LCD_RESET = 39;
const int W = 480, H = 480;
const uint8_t BRIGHTNESS = 110;  // AMOLED: keep it moderate

Arduino_DataBus *bus = nullptr;
Arduino_CO5300 *gfx = nullptr;

const uint16_t C_OK = RGB565(80, 220, 120), C_WAIT = RGB565(110, 110, 110), C_BAD = RGB565(240, 90, 80),
               C_DIM = RGB565(90, 90, 90), C_EYE = RGB565(240, 240, 240);

// --- eyes -------------------------------------------------------------------------------------------------------
// Drawn into a picture of the eye area in PSRAM, then sent to the panel in one go, so nothing flickers. Internal RAM
// is tight (the AirPlay mirror task needs an 8 KB block), so the picture must not live there.
const int AX = 20, AY = 80, AW = 440, AH = 270;  // the eye area on the screen
const int EYE_W = 84, EYE_H = 150, EYE_R = 42;    // one eye at rest: a tall pill
const int EYE_GAP = 180;                           // between the eyes' centres
const float REACH_X = 52, REACH_Y = 38;            // how far the eyes move from the middle
const uint32_t FRAME_MS = 40;
const uint32_t BLINK_MS = 200;
const uint32_t LOOK_HOLD_MS = 3000;  // after a look, before the eyes wander again

uint16_t *pic = nullptr;
float curX = 0, curY = 0;  // where the eyes look now, -1..1
float toX = 0, toY = 0;    // where they are going
uint32_t lastFrame = 0, blinkAt = 0, blinkStart = 0, wanderAt = 0, lookedAt = 0;
bool blinking = false, twice = false, drawn = false;

// fills a rounded rectangle centred on (cx, cy) into the picture
void eye(float cx, float cy, float w, float h) {
  float r = fminf(EYE_R * w / EYE_W, h / 2);
  int y0 = (int)lroundf(cy - h / 2), y1 = (int)lroundf(cy + h / 2);
  for (int y = max(y0, 0); y < min(y1, AH); y++) {
    float dy = 0;  // how far into a rounded corner this row is
    if (y < y0 + r) dy = y0 + r - (y + 0.5f);
    else if (y >= y1 - r) dy = (y + 0.5f) - (y1 - r);
    float inset = dy > 0 ? r - sqrtf(fmaxf(r * r - dy * dy, 0)) : 0;
    int x0 = max((int)lroundf(cx - w / 2 + inset), 0), x1 = min((int)lroundf(cx + w / 2 - inset), AW);
    uint16_t *row = pic + y * AW;
    for (int x = x0; x < x1; x++) row[x] = C_EYE;
  }
}

void drawEyes(float open) {
  memset(pic, 0, AW * AH * 2);
  float t = curX;  // the eye on the side it looks to comes closer, a little bigger
  float sl = 1 - 0.10f * t, sr = 1 + 0.10f * t;
  float cy = AH / 2 + curY * REACH_Y;
  float h = EYE_H * fmaxf(open, 0.07f);
  eye(AW / 2 - EYE_GAP / 2 + curX * REACH_X, cy, EYE_W * sl, h * sl);
  eye(AW / 2 + EYE_GAP / 2 + curX * REACH_X, cy, EYE_W * sr, h * sr);
  gfx->draw16bitRGBBitmap(AX, AY, pic, AW, AH);
}

uint32_t randomIn(uint32_t lo, uint32_t hi) { return lo + esp_random() % (hi - lo); }

// --- state lines ------------------------------------------------------------------------------------------------
String shownTop, shownBottom;

void dot(int x, int y, const char *label, uint16_t color) {
  gfx->fillCircle(x, y + 7, 6, color);
  gfx->setTextColor(color);
  gfx->setCursor(x + 14, y);
  gfx->print(label);
}
}  // namespace

bool screen_begin() {
  bus = new Arduino_ESP32QSPI(LCD_CS, LCD_SCLK, LCD_D0, LCD_D1, LCD_D2, LCD_D3);
  gfx = new Arduino_CO5300(bus, LCD_RESET, 0, W, H, 0, 0, 0, 0);
  if (!gfx->begin()) return false;
  bus->writeC8D8(0x36, 0xA0);  // memory access control, as in Waveshare's example
  gfx->fillScreen(RGB565_BLACK);
  gfx->setBrightness(BRIGHTNESS);
  gfx->setTextWrap(false);
  pic = (uint16_t *)heap_caps_malloc(AW * AH * 2, MALLOC_CAP_SPIRAM);
  blinkAt = millis() + 1500;
  wanderAt = millis() + 2500;
  return true;
}

void screen_look(float x, float y) {
  toX = constrain(x * 2 - 1, -1.f, 1.f);
  toY = constrain(y * 2 - 1, -1.f, 1.f);
  lookedAt = millis() ? millis() : 1;
}

void screen_tick() {
  if (!gfx || !pic) return;
  uint32_t now = millis();
  if (now - lastFrame < FRAME_MS) return;
  lastFrame = now;

  if (lookedAt && now - lookedAt > LOOK_HOLD_MS) {  // done looking: back towards the middle
    lookedAt = 0;
    toX *= 0.3f;
    toY *= 0.3f;
    wanderAt = now + randomIn(1500, 3500);
  }
  if (!lookedAt && (int32_t)(now - wanderAt) >= 0) {  // nothing to look at: a glance somewhere now and then
    toX = (randomIn(0, 1000) / 1000.f * 2 - 1) * 0.7f;
    toY = (randomIn(0, 1000) / 1000.f * 2 - 1) * 0.4f;
    if (randomIn(0, 3) == 0) toX = toY = 0;
    wanderAt = now + randomIn(2000, 5000);
  }
  if (!blinking && (int32_t)(now - blinkAt) >= 0) {
    blinking = true;
    blinkStart = now;
  }

  float open = 1;
  if (blinking) {
    float p = (now - blinkStart) / (float)BLINK_MS;  // closes, then opens
    if (p >= 1) {
      blinking = false;
      if (!twice && randomIn(0, 5) == 0) {  // now and then a second blink right after
        twice = true;
        blinkAt = now + 120;
      } else {
        twice = false;
        blinkAt = now + randomIn(2500, 6000);
      }
    } else {
      open = fabsf(1 - 2 * p);
    }
  }

  float dx = toX - curX, dy = toY - curY;
  bool moving = fabsf(dx) > 0.01f || fabsf(dy) > 0.01f;
  if (moving) {  // a quick start, a soft stop
    curX += dx * 0.45f;
    curY += dy * 0.45f;
  }
  if (moving || blinking || open < 1 || !drawn) {
    drawEyes(open);
    drawn = open >= 1 && !moving;
  }
}

void screen_show(const ScreenInfo &s) {
  if (!gfx) return;
  // top: version and address, small and dim
  String top = "fw " + s.fw + "   " + (s.net == "ap" ? s.ssid + " / " + s.pass : (s.net == "none" ? "no Wi-Fi" : s.ip));
  if (top != shownTop) {
    shownTop = top;
    gfx->fillRect(0, 30, W, 24, RGB565_BLACK);
    gfx->setTextSize(2);
    gfx->setTextColor(C_DIM);
    gfx->setCursor((W - (int)top.length() * 12) / 2, 34);
    gfx->print(top);
  }
  // bottom: a dot each for the phone's USB, Wi-Fi and mirroring
  int ap = constrain(s.airplay, 0, 2);
  uint16_t cu = s.usb == "ok" ? C_OK : (s.usb == "off" ? C_BAD : C_WAIT);
  uint16_t cw = s.net == "none" ? C_BAD : C_OK;
  uint16_t cm = ap == 2 ? C_OK : C_WAIT;
  String bottom = String(cu) + "," + cw + "," + cm + "," + ap;
  if (bottom != shownBottom) {
    shownBottom = bottom;
    gfx->fillRect(0, 380, W, 50, RGB565_BLACK);
    gfx->setTextSize(2);
    dot(70, 392, "USB", cu);
    dot(185, 392, "WiFi", cw);
    dot(310, 392, ap == 2 ? "Mirror" : (ap == 1 ? "AirPlay" : "Mirror"), cm);
  }
}

#else
bool screen_begin() { return false; }
void screen_show(const ScreenInfo &) {}
void screen_tick() {}
void screen_look(float, float) {}
#endif
