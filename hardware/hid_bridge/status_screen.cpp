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
const int AX = 20, AY = 70, AW = 440, AH = 300;  // the face's area on the screen
const int EYE_W = 84, EYE_H = 150, EYE_R = 42;    // one eye at rest: a tall pill
const int EYE_GAP = 180;                           // between the eyes' centres
const float REACH_X = 52, REACH_Y = 34;            // how far the eyes move from the middle
const float EYES_Y = 128, MOUTH_Y = 262;           // where the eyes and the mouth sit in the area
const float MOUTH_PEN = 4;                         // half the mouth line's thickness
const uint32_t FRAME_MS = 40;
const uint32_t BLINK_MS = 200, TAP_MS = 220;
const uint32_t LOOK_HOLD_MS = 3000;  // after a look, before the eyes wander again
// The face follows what the board does: commands for the phone make it focused, a pause after them thoughtful,
// a long quiet asleep (dim, eyes shut: an AMOLED keeps no picture that way). `face` sets a feeling for a while.
const uint32_t THINK_AFTER_MS = 1500, CALM_AFTER_MS = 45000, SLEEP_AFTER_MS = 180000;
const uint8_t SLEEP_BRIGHTNESS = 25;

// One eye's shape, as fractions of its height: lid covers the top (tilted by slant: > 0 lower on the outer side,
// sad; < 0 lower on the inner side, cross); happy cuts the bottom into an arch; raise moves it up (pixels).
struct Shape {
  float lid = 0, slant = 0, happy = 0, scale = 1, raise = 0;
};
// The mouth: a line w wide whose middle sits curve below its ends (> 0 a smile, < 0 a frown), and a ring of radius
// ring ("o"); shift moves it sideways.
struct Mouth {
  float w = 34, curve = 5, ring = 0, shift = 0;
};
struct Pose {
  Shape l, r;
  Mouth m;
  bool gaze = false;  // the mood decides where the eyes look
  float gx = 0, gy = 0;
  bool blinks = true;
  float open = 1;
};

Pose poseFor(Mood m) {
  Pose p;
  switch (m) {
    case MOOD_WORK:
      p.l.lid = p.r.lid = 0.22f;
      p.l.scale = p.r.scale = 0.96f;
      p.m.w = 24, p.m.curve = 0;
      break;
    case MOOD_THINK:
      p.l.lid = 0.28f;
      p.r.lid = 0.12f;
      p.gaze = true, p.gx = 0.55f, p.gy = -0.7f;
      p.m.w = 18, p.m.curve = -2, p.m.shift = 22;
      break;
    case MOOD_HAPPY:
      p.l.happy = p.r.happy = 1;
      p.l.scale = p.r.scale = 1.05f;
      p.l.raise = p.r.raise = 10;
      p.gaze = true, p.gy = -0.2f;
      p.m.w = 64, p.m.curve = 20;
      break;
    case MOOD_ASK:  // one eye wide and up, the other narrowed: "hm?"
      p.l.scale = 1.12f, p.l.raise = 14;
      p.r.scale = 0.92f, p.r.lid = 0.32f, p.r.slant = -0.12f;
      p.gaze = true, p.gx = 0.25f, p.gy = -0.35f;
      p.m.w = 0, p.m.curve = 0, p.m.ring = 10, p.m.shift = 8;
      break;
    case MOOD_SAD:
      p.l.lid = p.r.lid = 0.2f;
      p.l.slant = p.r.slant = 0.28f;
      p.l.scale = p.r.scale = 0.95f;
      p.l.raise = p.r.raise = -10;
      p.gaze = true, p.gy = 0.55f;
      p.m.w = 40, p.m.curve = -11;
      break;
    case MOOD_SLEEP:
      p.open = 0.06f;
      p.blinks = false;
      p.gaze = true, p.gy = 0.35f;
      p.m.w = 14, p.m.curve = 0;
      break;
    default:
      break;
  }
  return p;
}

uint16_t *pic = nullptr;
float curX = 0, curY = 0;  // where the eyes look now, -1..1
float toX = 0, toY = 0;    // where they are going
Shape curL, curR;
Mouth curM;
float curOpen = 1;
Mood mood = MOOD_NEUTRAL;
uint32_t moodUntil = 0;  // a feeling set by `face` lasts until then (0: until something else happens)
uint32_t lastFrame = 0, blinkAt = 0, blinkStart = 0, wanderAt = 0, lookedAt = 0, tapStart = 0, activeAt = 0;
bool blinking = false, twice = false, tapping = false, drawn = false, dim = false;

void setMood(Mood m, uint32_t forMs) {
  mood = m;
  moodUntil = forMs ? (millis() + forMs) | 1 : 0;
}

// fills one eye centred on (cx, cy) into the picture: a rounded rectangle, less what the lid and the arch cut away
void eye(float cx, float cy, float w, float h, const Shape &s, bool left) {
  float r = fminf(EYE_R * w / EYE_W, h / 2);
  float top = cy - h / 2, bottom = cy + h / 2;
  int y0 = (int)lroundf(top), y1 = (int)lroundf(bottom);
  for (int y = max(y0, 0); y < min(y1, AH); y++) {
    float yc = y + 0.5f;
    float dy = 0;  // how far into a rounded corner this row is
    if (yc < top + r) dy = top + r - yc;
    else if (yc > bottom - r) dy = yc - (bottom - r);
    float inset = dy > 0 ? r - sqrtf(fmaxf(r * r - dy * dy, 0)) : 0;
    int x0 = max((int)lroundf(cx - w / 2 + inset), 0), x1 = min((int)lroundf(cx + w / 2 - inset), AW);
    uint16_t *row = pic + y * AW;
    for (int x = x0; x < x1; x++) {
      float dx = (x + 0.5f - cx) / (w / 2);  // -1..1 across the eye
      float outer = left ? -dx : dx;
      if (yc < top + h * (s.lid + s.slant * outer)) continue;
      if (s.happy > 0 && yc > bottom - h * s.happy * (0.72f - 0.42f * dx * dx)) continue;
      row[x] = C_EYE;
    }
  }
}

// a round dot of radius r, the mouth's pen
void disc(float cx, float cy, float r) {
  for (int y = max((int)(cy - r), 0); y <= min((int)(cy + r), AH - 1); y++) {
    float dy = y + 0.5f - cy, half = sqrtf(fmaxf(r * r - dy * dy, 0));
    uint16_t *row = pic + y * AW;
    for (int x = max((int)lroundf(cx - half), 0); x < min((int)lroundf(cx + half), AW); x++) row[x] = C_EYE;
  }
}

void mouth(float cx, float cy, const Mouth &m) {
  if (m.w > 1) {  // the line: a parabola drawn with the pen
    int n = (int)m.w;
    for (int i = 0; i <= n; i++) {
      float u = i / (float)n * 2 - 1;  // -1..1 along it
      disc(cx + u * m.w / 2, cy + m.curve * (1 - u * u) - m.curve / 2, MOUTH_PEN);
    }
  }
  if (m.ring > 1) {  // the "o"
    int n = (int)(m.ring * 6);
    for (int i = 0; i < n; i++) {
      float a = i * 2 * (float)M_PI / n;
      disc(cx + m.ring * cosf(a), cy + m.ring * 1.15f * sinf(a), MOUTH_PEN * 0.8f);
    }
  }
}

void drawFace(float open) {
  memset(pic, 0, AW * AH * 2);
  float t = curX;  // the eye on the side it looks to comes closer, a little bigger
  float sl = (1 - 0.10f * t) * curL.scale, sr = (1 + 0.10f * t) * curR.scale;
  float cy = EYES_Y + curY * REACH_Y;
  float h = EYE_H * fmaxf(open, 0.06f);
  eye(AW / 2 - EYE_GAP / 2 + curX * REACH_X, cy - curL.raise, EYE_W * sl, h * sl, curL, true);
  eye(AW / 2 + EYE_GAP / 2 + curX * REACH_X, cy - curR.raise, EYE_W * sr, h * sr, curR, false);
  mouth(AW / 2 + curX * REACH_X * 0.6f + curM.shift, MOUTH_Y + curY * 12, curM);  // turns with the eyes, less far
  gfx->draw16bitRGBBitmap(AX, AY, pic, AW, AH);
}

uint32_t randomIn(uint32_t lo, uint32_t hi) { return lo + esp_random() % (hi - lo); }

// moves a towards b; true while it still moves
bool ease(float &a, float b, float k = 0.35f) {
  float d = b - a;
  if (fabsf(d) < 0.004f) {
    a = b;
    return false;
  }
  a += d * k;
  return true;
}

bool easeMouth(Mouth &a, const Mouth &b) {
  bool m = ease(a.w, b.w, 0.3f);
  m |= ease(a.curve, b.curve, 0.3f);
  m |= ease(a.ring, b.ring, 0.3f);
  m |= ease(a.shift, b.shift, 0.3f);
  return m;
}

bool easeShape(Shape &a, const Shape &b) {
  bool m = ease(a.lid, b.lid);
  m |= ease(a.slant, b.slant);
  m |= ease(a.happy, b.happy);
  m |= ease(a.scale, b.scale);
  m |= ease(a.raise, b.raise);
  return m;
}

void wake() {
  activeAt = millis() | 1;
  if (mood == MOOD_SLEEP) {
    mood = MOOD_NEUTRAL;
    blinkAt = millis() + 300;  // a blink on waking
  }
  if (dim) {
    dim = false;
    gfx->setBrightness(BRIGHTNESS);
  }
}

// --- state lines ---------------------------------------------------------------------------------------------
const int DOT_X = 360, DOT_Y = 48, DOT_GAP = 24, DOT_R = 7;  // inside the panel's rounded corner
String shownInfo;
uint16_t shownDot[3] = {1, 1, 1};  // nothing drawn yet

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
  activeAt = millis() | 1;
  return true;
}

void screen_look(float x, float y) {
  if (!gfx) return;
  wake();
  toX = constrain(x * 2 - 1, -1.f, 1.f);
  toY = constrain(y * 2 - 1, -1.f, 1.f);
  lookedAt = millis() | 1;
}

void screen_active() {
  if (!gfx) return;
  wake();
  setMood(MOOD_WORK, 0);
}

void screen_tap() {
  if (!gfx) return;
  screen_active();
  tapping = true;
  tapStart = millis();
}

void screen_face(Mood m, uint32_t forMs) {
  if (!gfx) return;
  wake();
  setMood(m, forMs);
  if (m == MOOD_SLEEP) activeAt = 0;
}

void screen_tick() {
  if (!gfx || !pic) return;
  uint32_t now = millis();
  if (now - lastFrame < FRAME_MS) return;
  lastFrame = now;

  // the mood moves on by itself
  if (moodUntil && (int32_t)(now - moodUntil) >= 0) setMood(MOOD_NEUTRAL, 0);
  if (!moodUntil && activeAt) {
    uint32_t quiet = now - activeAt;
    if (mood == MOOD_WORK && quiet > THINK_AFTER_MS) setMood(MOOD_THINK, 0);
    else if (mood == MOOD_THINK && quiet > CALM_AFTER_MS) setMood(MOOD_NEUTRAL, 0);
  }
  if (mood != MOOD_SLEEP && now - activeAt > SLEEP_AFTER_MS && (!moodUntil || mood == MOOD_NEUTRAL)) {
    setMood(MOOD_SLEEP, 0);
  }
  if (mood == MOOD_SLEEP && !dim && curOpen < 0.1f) {
    dim = true;
    gfx->setBrightness(SLEEP_BRIGHTNESS);
  }
  Pose pose = poseFor(mood);

  // where to look
  if (lookedAt && now - lookedAt > LOOK_HOLD_MS) {  // done looking: back towards the middle
    lookedAt = 0;
    toX *= 0.3f;
    toY *= 0.3f;
    wanderAt = now + randomIn(1500, 3500);
  }
  if (pose.gaze && !(lookedAt && mood == MOOD_WORK)) {
    toX = pose.gx;
    toY = pose.gy;
  } else if (!lookedAt && (int32_t)(now - wanderAt) >= 0) {  // nothing to look at: a glance somewhere now and then
    toX = (randomIn(0, 1000) / 1000.f * 2 - 1) * 0.7f;
    toY = (randomIn(0, 1000) / 1000.f * 2 - 1) * 0.4f;
    if (randomIn(0, 3) == 0) toX = toY = 0;
    wanderAt = now + randomIn(2000, 5000);
  }

  // blinks, and a squeeze on each tap
  if (!blinking && pose.blinks && (int32_t)(now - blinkAt) >= 0) {
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
  if (tapping) {
    float p = (now - tapStart) / (float)TAP_MS;
    if (p >= 1) tapping = false;
    else open = fminf(open, 1 - 0.4f * sinf(p * (float)M_PI));
  }

  bool moving = ease(curX, toX, 0.45f);
  moving |= ease(curY, toY, 0.45f);
  moving |= easeShape(curL, pose.l);
  moving |= easeShape(curR, pose.r);
  moving |= easeMouth(curM, pose.m);
  moving |= ease(curOpen, pose.open, 0.25f);
  float shown = open * curOpen;
  if (moving || blinking || tapping || !drawn) {
    drawFace(shown);
    drawn = !moving && !blinking && !tapping;
  }
}

void screen_show(const ScreenInfo &s) {
  if (!gfx) return;
  // bottom: version and address, small and dim
  String info = "fw " + s.fw + "   " + (s.net == "ap" ? s.ssid + " / " + s.pass : (s.net == "none" ? "no Wi-Fi" : s.ip));
  if (info != shownInfo) {
    shownInfo = info;
    gfx->fillRect(0, 395, W, 24, RGB565_BLACK);
    gfx->setTextSize(2);
    gfx->setTextColor(C_DIM);
    gfx->setCursor((W - (int)info.length() * 12) / 2, 399);
    gfx->print(info);
  }
  // top right: a dot each for the phone's USB, Wi-Fi and mirroring, no words
  int ap = constrain(s.airplay, 0, 2);
  uint16_t c[3] = {s.usb == "ok" ? C_OK : (s.usb == "off" ? C_BAD : C_WAIT), s.net == "none" ? C_BAD : C_OK,
                   ap == 2 ? C_OK : C_WAIT};
  for (int i = 0; i < 3; i++) {
    if (c[i] == shownDot[i]) continue;
    shownDot[i] = c[i];
    gfx->fillCircle(DOT_X + i * DOT_GAP, DOT_Y, DOT_R, c[i]);
  }
}

#else
bool screen_begin() { return false; }
void screen_show(const ScreenInfo &) {}
void screen_tick() {}
void screen_look(float, float) {}
void screen_active() {}
void screen_tap() {}
void screen_face(Mood, uint32_t) {}
#endif
