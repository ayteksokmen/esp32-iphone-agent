// See status_screen.h. Pins and panel init follow Waveshare's 01_HelloWorld example for this board
// (github.com/waveshareteam/ESP32-S3-Touch-AMOLED-2.16, GFX Library for Arduino 1.6.4).
#include "status_screen.h"

#if BOARD_AMOLED_216
#include <Arduino_GFX_Library.h>
#include <esp_heap_caps.h>
#include "driver/i2c_master.h"
#include "freertos/idf_additions.h"
#include <math.h>

namespace {
const int LCD_CS = 12, LCD_SCLK = 38, LCD_D0 = 4, LCD_D1 = 5, LCD_D2 = 6, LCD_D3 = 7, LCD_RESET = 39;
const int W = 480, H = 480;
const uint8_t BRIGHTNESS = 110;  // AMOLED: keep it moderate

Arduino_DataBus *bus = nullptr;
Arduino_CO5300 *gfx = nullptr;

const uint16_t C_OK = RGB565(80, 220, 120), C_WAIT = RGB565(110, 110, 110), C_BAD = RGB565(240, 90, 80),
               C_DIM = RGB565(90, 90, 90), C_EYE = RGB565(250, 199, 117);  // warm amber

// --- eyes -------------------------------------------------------------------------------------------------------
// Drawn into a picture of the eye area in PSRAM, then sent to the panel in one go, so nothing flickers. Internal RAM
// is tight (the AirPlay mirror task needs an 8 KB block), so the picture must not live there.
const int AX = 20, AY = 70, AW = 440, AH = 300;  // the face's area on the screen
const int EYE_W = 104, EYE_H = 124, EYE_R = 36;   // one eye at rest: a rounded square
const int EYE_GAP = 190;                           // between the eyes' centres
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
  float fill = 0;  // > 0.5: the ring is a filled oval, a wide-open mouth
};
struct Pose {
  Shape l, r;
  Mouth m;
  bool gaze = false;  // the mood decides where the eyes look
  float gx = 0, gy = 0;
  bool blinks = true;
  float open = 1;
};

// Waking up, after boot (in step with boot_sound.cpp's times): eyes shut, a yawn (eyes squeezed into arches, the
// mouth a wide-open oval), drowsy, then two blinks and awake.
const uint32_t WAKE_YAWN_MS = 250, WAKE_DROWSY_MS = 1350, WAKE_DONE_MS = 1650;

Pose wakePose(uint32_t t) {
  Pose p;
  p.blinks = false;
  p.gaze = true;
  if (t < WAKE_YAWN_MS) {  // still asleep
    p.open = 0.06f;
    p.gy = 0.35f;
    p.m.w = 14, p.m.curve = 0;
  } else if (t < WAKE_DROWSY_MS) {  // the yawn
    p.open = 0.5f;
    p.l.happy = p.r.happy = 1;
    p.l.raise = p.r.raise = 8;
    p.gy = -0.35f;
    p.m.w = 0, p.m.curve = 0, p.m.ring = 22, p.m.fill = 1;
  } else {  // drowsy
    p.l.lid = p.r.lid = 0.45f;
    p.gy = 0.1f;
    p.m.w = 16, p.m.curve = 0;
  }
  return p;
}

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
uint32_t wakeStart = 0;  // the wake-up animation runs from then (0: not)
uint32_t lastFrame = 0, blinkAt = 0, blinkStart = 0, wanderAt = 0, lookedAt = 0, tapStart = 0, activeAt = 0;
bool wakeBlinks = false;  // the blink after waking up comes twice
bool blinking = false, twice = false, tapping = false, drawn = false, dim = false;

void setMood(Mood m, uint32_t forMs) {
  mood = m;
  moodUntil = forMs ? (millis() + forMs) | 1 : 0;
}

// Pupils: dark discs inside the eyes that look further than the eyes move, at where the face looks (curX, curY),
// with a small light dot. Only on what is left of the eye after lids and arches, and not while it is nearly shut.
const float PUPIL = 0.24f;  // radius, a fraction of the eye's smaller side

void pupil(float cx, float cy, float w, float h, float gx, float gy) {
  float r = PUPIL * fminf(w, h);
  float px = cx + gx * (w / 2 - r - 8), py = cy + gy * (h / 2 - r - 8);
  for (int y = max((int)(py - r), 0); y <= min((int)(py + r), AH - 1); y++) {
    float dy = y + 0.5f - py, half = sqrtf(fmaxf(r * r - dy * dy, 0));
    uint16_t *row = pic + y * AW;
    for (int x = max((int)lroundf(px - half), 0); x < min((int)lroundf(px + half), AW); x++) {
      if (row[x] != C_EYE) continue;  // outside what is left of the eye
      float hx = x + 0.5f - (px + r * 0.35f), hy = y + 0.5f - (py - r * 0.35f);
      row[x] = hx * hx + hy * hy < r * r * 0.08f ? C_EYE : RGB565_BLACK;  // the light dot
    }
  }
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
  if (m.ring > 1 && m.fill > 0.5f) {  // wide open: a filled oval
    float rx = m.ring, ry = m.ring * 1.25f;
    for (int y = max((int)(cy - ry), 0); y <= min((int)(cy + ry), AH - 1); y++) {
      float dy = (y + 0.5f - cy) / ry, half = rx * sqrtf(fmaxf(1 - dy * dy, 0));
      uint16_t *row = pic + y * AW;
      for (int x = max((int)lroundf(cx - half), 0); x < min((int)lroundf(cx + half), AW); x++) row[x] = C_EYE;
    }
  } else if (m.ring > 1) {  // the "o"
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
  if (open > 0.4f && curL.happy < 0.4f) {  // no pupils in a blink, asleep, or in happy arches
    pupil(AW / 2 - EYE_GAP / 2 + curX * REACH_X, cy - curL.raise, EYE_W * sl, h * sl, curX, curY);
    pupil(AW / 2 + EYE_GAP / 2 + curX * REACH_X, cy - curR.raise, EYE_W * sr, h * sr, curX, curY);
  }
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
  a.fill = b.fill;
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

// --- pages -------------------------------------------------------------------------------------------------------
// Page 0 is the face alone. Page 1 is the board's state, in words. A swipe up turns to it, a swipe down back to the
// face; two small dots on the right edge show which page is up. The state page goes back to the face after a while
// untouched.
const uint32_t STATE_PAGE_MS = 30000;
const int PAGE_DOT_X = 458, PAGE_DOT_R = 5, PAGE_DOT_GAP = 22;
const uint16_t C_PAGE_ON = RGB565(200, 200, 200), C_PAGE_OFF = RGB565(70, 70, 70);
const int LINE_X = 48, LINE_Y = 70, LINE_H = 44, LINES = 7;

int page = 0;
uint32_t pageAt = 0;
ScreenInfo info;  // the latest state, for page 1
bool haveInfo = false;
String shown[LINES];
uint16_t shownColor[LINES];

void pageDots() {
  for (int i = 0; i < 2; i++)
    gfx->fillCircle(PAGE_DOT_X, H / 2 - PAGE_DOT_GAP / 2 + i * PAGE_DOT_GAP, PAGE_DOT_R, i == page ? C_PAGE_ON : C_PAGE_OFF);
}

void line(int i, const String &text, uint16_t color) {
  if (shown[i] == text && shownColor[i] == color) return;
  shown[i] = text;
  shownColor[i] = color;
  int y = LINE_Y + i * LINE_H;
  gfx->fillRect(0, y, W, LINE_H, RGB565_BLACK);
  gfx->setTextSize(3);
  gfx->setTextColor(color);
  gfx->setCursor(LINE_X, y + 10);
  gfx->print(text);
}

void drawState() {
  if (!haveInfo) return;
  const ScreenInfo &s = info;
  static const char *const apText[] = {"waiting", "connected", "mirroring"};
  int ap = constrain(s.airplay, 0, 2);
  line(0, "USB    " + String(s.usb == "ok" ? "connected" : s.usb), s.usb == "ok" ? C_OK : (s.usb == "off" ? C_BAD : C_WAIT));
  line(1, "WiFi   " + String(s.net == "none" ? "none" : (s.net == "ap" ? "hotspot" : "home")), s.net == "none" ? C_BAD : C_OK);
  line(2, "       " + (s.net == "ap" ? s.ssid : s.ip), C_DIM);
  line(3, "Mirror " + String(apText[ap]), ap == 2 ? C_OK : C_WAIT);
  line(4, ap == 2 ? "       " + String(s.fps, 1) + " fps" : "", C_DIM);
  line(5, "Link   " + String(s.cli ? "computer" : (s.ble ? "app" : "none")), s.cli || s.ble ? C_OK : C_WAIT);
  line(6, "fw " + s.fw, C_DIM);
}

void showPage(int p) {
  page = p;
  pageAt = millis();
  gfx->fillScreen(RGB565_BLACK);
  for (int i = 0; i < LINES; i++) shown[i] = "\x01";  // redraw every line
  if (page == 1) drawState();
  else drawn = false;  // the face draws itself on the next tick
  pageDots();
}

// --- touch -------------------------------------------------------------------------------------------------------
// The CST9220 touch controller, on the codecs' I2C bus (SDA 15, SCL 14, address 0x5A, interrupt 11, reset 40), read
// the way SensorLib's TouchDrvCST92xx does. The loop reads it after each interrupt (a report while a finger is
// down); a pause in them means the finger lifted. Only one finger and only swipes matter here.
// It has its own ESP-IDF I2C master on the second controller: through Arduino's Wire, every transfer failed (error
// 4 on every address) about a second after boot though the lines were idle high, and starting Wire over hung the
// loop until the watchdog reset the board (2026-11-15). The boot sound uses Wire on the first controller and is
// done with it before this starts.
const int TP_INT = 11, TP_RST = 40, TP_ADDR = 0x5A;
const int SWIPE_MIN = 70;           // pixels up or down
const uint32_t TOUCH_LIFT_MS = 120;  // no report for this long: the finger lifted
bool touchOk = false, touching = false;
volatile bool touchIrq = false;
volatile uint32_t touchIrqs = 0;
uint32_t touchReads = 0, touchFails = 0, touchAt = 0;
uint32_t touchSwipes = 0, touchChip = 0, touchCheck = 0;
int touchX0 = 0, touchY0 = 0, touchX = 0, touchY = 0;

i2c_master_bus_handle_t touchBus = nullptr;
i2c_master_dev_handle_t touchDev = nullptr;
const int TP_TIMEOUT_MS = 20;
esp_err_t touchLastErr = ESP_OK;

// a two-byte register read, as SensorLib's writeThenRead does it
bool touchReg(uint8_t hi, uint8_t lo, uint8_t *out, int n) {
  uint8_t w[2] = {hi, lo};
  touchLastErr = i2c_master_transmit_receive(touchDev, w, 2, out, n, TP_TIMEOUT_MS);
  return touchLastErr == ESP_OK;
}

void touchCmd(uint8_t hi, uint8_t lo, uint8_t v = 0, int len = 2) {
  uint8_t w[3] = {hi, lo, v};
  touchLastErr = i2c_master_transmit(touchDev, w, len, TP_TIMEOUT_MS);
}

void IRAM_ATTR onTouch() {
  touchIrqs++;
  touchIrq = true;
}

// one finger's position (screen pixels), false when no finger is down
bool touchRead(int &x, int &y) {
  uint8_t buf[12] = {0};
  if (!touchReg(0xD0, 0x00, buf, sizeof buf)) {
    touchFails++;
    return false;
  }
  touchReads++;
  touchCmd(0xD0, 0x00, 0xAB, 3);
  if (buf[6] != 0xAB) return false;
  int n = buf[5] & 0x7F;
  if (n == 0 || n > 2 || (buf[0] & 0x0F) != 0x06) return false;
  int rx = (buf[1] << 4) | (buf[3] >> 4), ry = (buf[2] << 4) | (buf[3] & 0x0F);
  x = W - 1 - ry;  // the panel's axes: swapped, and x mirrored (Waveshare's example)
  y = rx;
  return true;
}

void touchPoll(uint32_t now) {
  if (!touchOk) return;
  int x, y;
  if (touchIrq) {
    touchIrq = false;
    if (touchRead(x, y)) touchAt = now;
    else return;
    if (!touching) {
      touching = true;
      touchX0 = x, touchY0 = y;
      wake();  // a touch wakes the face up
    }
    touchX = x, touchY = y;
    pageAt = now;
    return;
  }
  if (!touching || now - touchAt < TOUCH_LIFT_MS) return;
  touching = false;  // the finger lifted: was it a swipe?
  int dx = touchX - touchX0, dy = touchY - touchY0;
  if (abs(dy) >= SWIPE_MIN && abs(dy) > 2 * abs(dx)) {  // up: the state page; down: the face
    touchSwipes++;
    showPage(dy < 0 ? 1 : 0);
  }
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
  activeAt = millis() | 1;
  pageDots();
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

void screen_wakeup() {
  if (!gfx) return;
  wakeStart = millis() | 1;
  curOpen = 0.06f;  // start asleep
  curM.w = 14, curM.curve = 0;
  drawn = false;
}

void screen_tick() {
  if (!gfx || !pic) return;
  uint32_t now = millis();
  touchPoll(now);
  if (now - lastFrame < FRAME_MS) return;
  lastFrame = now;
  if (page == 1) {
    if (!touching && now - pageAt > STATE_PAGE_MS) showPage(0);  // back to the face
    else return;  // the face waits behind the state page
  }

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
  if (wakeStart) {
    uint32_t t = now - wakeStart;
    if (t < WAKE_DONE_MS) {
      pose = wakePose(t);
      lookedAt = 0;
    } else {  // awake: two blinks, then the usual
      wakeStart = 0;
      setMood(MOOD_NEUTRAL, 0);
      activeAt = now | 1;
      blinkAt = now + 80;
      twice = false;
      wakeBlinks = true;
    }
  }

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
      if (!twice && (wakeBlinks || randomIn(0, 5) == 0)) {  // now and then a second blink right after
        wakeBlinks = false;
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
  info = s;
  haveInfo = true;
  if (page == 1) drawState();
}

String screen_i2c_scan() {  // debugging: who answers on the touch chip's bus
  if (!touchBus) return "no bus";
  String r = "ok";
  for (int a = 0x08; a < 0x78; a++)
    if (i2c_master_probe(touchBus, a, TP_TIMEOUT_MS) == ESP_OK) r += " " + String(a, HEX);
  return r;
}

String screen_touch_info() {
  char b[220];
  snprintf(b, sizeof b, "ok %d chip %lx check %lx err %d irqs %lu reads %lu fails %lu swipes %lu page %d last %d,%d from %d,%d",
           touchOk, (unsigned long)touchChip, (unsigned long)touchCheck, (int)touchLastErr, (unsigned long)touchIrqs,
           (unsigned long)touchReads, (unsigned long)touchFails, (unsigned long)touchSwipes, page, touchX, touchY,
           touchX0, touchY0);
  return b;
}

void screen_touch_begin() {
  if (!gfx || touchOk) return;
  pinMode(TP_RST, OUTPUT);
  digitalWrite(TP_RST, LOW);
  delay(10);
  digitalWrite(TP_RST, HIGH);
  delay(30);  // out of its boot mode
  i2c_master_bus_config_t bc = {};
  bc.i2c_port = I2C_NUM_1;
  bc.sda_io_num = GPIO_NUM_15;
  bc.scl_io_num = GPIO_NUM_14;
  bc.clk_source = I2C_CLK_SRC_DEFAULT;
  bc.glitch_ignore_cnt = 7;
  bc.flags.enable_internal_pullup = true;
  if ((touchLastErr = i2c_new_master_bus(&bc, &touchBus)) != ESP_OK) return;
  i2c_device_config_t dc = {};
  dc.dev_addr_length = I2C_ADDR_BIT_LEN_7;
  dc.device_address = TP_ADDR;
  dc.scl_speed_hz = 100000;  // as Waveshare's example
  if ((touchLastErr = i2c_master_bus_add_device(touchBus, &dc, &touchDev)) != ESP_OK) return;
  // as SensorLib's begin: command mode, the chip's check code and type (CST9220 / CST9217), then normal mode
  touchCmd(0xD1, 0x01);
  if (touchLastErr != ESP_OK) return;
  delay(10);
  uint8_t b[4] = {0};
  if (touchReg(0xD1, 0xFC, b, 4)) touchCheck = b[0] | b[1] << 8 | b[2] << 16 | (uint32_t)b[3] << 24;
  if (touchReg(0xD2, 0x04, b, 4)) touchChip = b[2] | b[3] << 8;
  touchCmd(0xD1, 0x09);
  touchOk = true;
  pinMode(TP_INT, INPUT_PULLUP);
  attachInterrupt(digitalPinToInterrupt(TP_INT), onTouch, FALLING);
}

#else
bool screen_begin() { return false; }
void screen_show(const ScreenInfo &) {}
void screen_wakeup() {}
void screen_tick() {}
void screen_touch_begin() {}
String screen_touch_info() { return "no screen"; }
String screen_i2c_scan() { return "no screen"; }
void screen_look(float, float) {}
void screen_active() {}
void screen_tap() {}
void screen_face(Mood, uint32_t) {}
#endif
