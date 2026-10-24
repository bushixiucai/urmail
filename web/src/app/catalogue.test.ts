import { createInstance } from "i18next";
import { describe, expect, it } from "vitest";
import { adaptCatalogue } from "./catalogue";

describe("shared backend catalog interpolation", () => {
  it("preserves existing frontend syntax, literal text and JSON", () => {
    const original = {
      single: "Order {number}",
      double: "Order {{number}}",
      adjacent: "{first}{last}",
      mixed: "{first} + {{last}}",
      json: '{"kind":"notice","days":3}',
      literal: "Use { ordinary text }, {not-a-placeholder} and {{nested}}.",
      malformed: "{{{number}}} and {{number} and {number}}",
    };
    expect(adaptCatalogue(original)).toEqual({
      single: "Order {{number}}",
      double: "Order {{number}}",
      adjacent: "{{first}}{{last}}",
      mixed: "{{first}} + {{last}}",
      json: original.json,
      literal: original.literal,
      malformed: original.malformed,
    });
    expect(original.single).toBe("Order {number}");
    expect(adaptCatalogue(adaptCatalogue(original))).toEqual(
      adaptCatalogue(original),
    );
  });

  for (const [locale, message, expected] of [
    [
      "en",
      "Support replied to ticket {ticket}; expiry in {days} days.",
      "Support replied to ticket 481; expiry in 3 days.",
    ],
    [
      "zh-CN",
      "工单 {ticket} 已回复；还有 {days} 天到期。",
      "工单 481 已回复；还有 3 天到期。",
    ],
  ]) {
    it(`${locale} renders real notification parameters through i18next`, async () => {
      const instance = createInstance();
      await instance.init({
        lng: locale,
        resources: {
          [locale]: {
            translation: adaptCatalogue({
              "notification.support_reply": message,
              paid: "Order {{number}}",
            }),
          },
        },
        interpolation: { escapeValue: false },
      });
      expect(
        instance.t("notification.support_reply", { ticket: 481, days: 3 }),
      ).toBe(expected);
      expect(instance.t("paid", { number: "ORDER-481" })).toBe(
        "Order ORDER-481",
      );
    });
  }
});
