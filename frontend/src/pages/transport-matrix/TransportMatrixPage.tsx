import { memo } from "react";
import { PageContainer } from "@/components/ui/page-container";
import { SectionCard } from "@/components/ui/section-card";
import { Badge } from "@/components/ui/badge";
import { Banner } from "@/components/ui/banner";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { useI18n } from "@/lib/i18n";
import { qualityLabelKey } from "@/pages/payload-generator/components/transport-quality";
import { TRANSPORT_MATRIX_ROWS } from "./transport-matrix-data";

type TKey = (key: string, params?: Record<string, string | number>) => string;

const QUALITY_VARIANT = {
  core: "secondary",
  hardened: "default",
  experimental: "outline",
} as const;

/**
 * Static transport reference: the published assurance level of each beacon
 * transport. Quality comes from transport-quality.ts (kept in sync with the
 * capability matrix by check:transport-matrix); notes are i18n keys in the base
 * dictionary. No live API — this is documentation surfaced in the console.
 */
function TransportMatrixPage() {
  const { t } = useI18n() as { t: TKey };

  return (
    <PageContainer title={t("transport.title")} subtitle={t("transport.subtitle")}>
      <Banner tone="info" className="mb-4">
        {t("transport.subtitle")}
      </Banner>
      <SectionCard title={t("transport.title")}>
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>{t("transport.col_transport")}</TableHead>
              <TableHead>{t("transport.col_quality")}</TableHead>
              <TableHead>{t("transport.col_capability")}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {TRANSPORT_MATRIX_ROWS.map((row) => (
              <TableRow key={row.value}>
                <TableCell className="font-medium">{row.label}</TableCell>
                <TableCell>
                  <Badge variant={QUALITY_VARIANT[row.quality]}>
                    {t(qualityLabelKey(row.quality))}
                  </Badge>
                </TableCell>
                <TableCell className="text-muted-foreground">
                  {row.noteKeys.map((k) => t(`transport.note_${k}`)).join(" ")}
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </SectionCard>
    </PageContainer>
  );
}

export default memo(TransportMatrixPage);
