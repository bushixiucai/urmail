import { Link } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";

/**
 * Client auto-configuration and collaboration setup guides. Third-round
 * client matrix only: iOS/iPadOS and macOS native apps, Thunderbird, and
 * Android via DAVx⁵ plus one EAS client. No unspecified-app promises.
 */

export const MAIL_HOST = "box.urmail.cn";
export const EAS_URL = `https://${MAIL_HOST}/api/v1/mailbox/eas`;

export interface GuideSection {
  id: string;
  titleKey: string;
  stepKeys: string[];
}

export const GUIDE_SECTIONS: GuideSection[] = [
  {
    id: "ios",
    titleKey: "page.guides.ios.title",
    stepKeys: [
      "page.guides.ios.s1",
      "page.guides.ios.s2",
      "page.guides.ios.s3",
      "page.guides.ios.s4",
    ],
  },
  {
    id: "macos",
    titleKey: "page.guides.macos.title",
    stepKeys: ["page.guides.macos.s1", "page.guides.macos.s2"],
  },
  {
    id: "thunderbird",
    titleKey: "page.guides.thunderbird.title",
    stepKeys: [
      "page.guides.thunderbird.s1",
      "page.guides.thunderbird.s2",
      "page.guides.thunderbird.s3",
    ],
  },
  {
    id: "davx5",
    titleKey: "page.guides.davx5.title",
    stepKeys: ["page.guides.davx5.s1", "page.guides.davx5.s2"],
  },
  {
    id: "android_eas",
    titleKey: "page.guides.android_eas.title",
    stepKeys: ["page.guides.android_eas.s1", "page.guides.android_eas.s2"],
  },
];

export function allGuideKeys(): string[] {
  const keys: string[] = [
    "page.guides.title",
    "page.guides.intro",
    "page.guides.server",
    "page.guides.ports",
    "page.guides.help_link",
    "page.guides.eas_caveat",
    "page.guides.davx5.note",
    "page.guides.thunderbird.note",
  ];
  for (const section of GUIDE_SECTIONS) {
    keys.push(section.titleKey, ...section.stepKeys);
  }
  return keys;
}

export function ClientGuidesPage() {
  const { t } = useTranslation();

  return (
    <Card>
      <CardHeader>
        <CardTitle>{t("page.guides.title")}</CardTitle>
      </CardHeader>
      <CardContent className="space-y-6 text-sm">
        <p className="text-muted-foreground">{t("page.guides.intro")}</p>
        <div className="flex flex-wrap gap-x-2 gap-y-1">
          <span className="font-medium">{t("page.guides.server")}</span>
          <code className="rounded bg-muted px-1.5 py-0.5 font-mono text-xs">
            {MAIL_HOST}
          </code>
          <span className="text-muted-foreground">
            {t("page.guides.ports")}
          </span>
        </div>
        {GUIDE_SECTIONS.map((section) => (
          <section key={section.id} className="space-y-2">
            <h2 className="text-base font-semibold">{t(section.titleKey)}</h2>
            <ol className="list-decimal space-y-1 pl-5">
              {section.stepKeys.map((key) => (
                <li key={key}>{t(key)}</li>
              ))}
            </ol>
            {section.id === "thunderbird" && (
              <p className="text-xs text-muted-foreground">
                {t("page.guides.thunderbird.note")}
              </p>
            )}
            {section.id === "davx5" && (
              <p className="text-xs text-muted-foreground">
                {t("page.guides.davx5.note")}
              </p>
            )}
          </section>
        ))}
        <p className="rounded border border-amber-300 bg-amber-50 p-3 text-xs dark:border-amber-700 dark:bg-amber-950">
          {t("page.guides.eas_caveat")}
        </p>
        <p>
          <Link to="/webmail/help" className="text-primary underline">
            {t("page.guides.help_link")}
          </Link>
        </p>
      </CardContent>
    </Card>
  );
}
