import { useScanLibrary } from "../../hooks/use-library";
import { toast } from "sonner";
import Button from "../../components/Button";

export default function ScanButton() {
  const scan = useScanLibrary();

  const handleScan = () => {
    scan.mutate(undefined, {
      onSuccess: (stats) => {
        toast.success(
          `Scan complete: scanned ${stats.scanned}, imported ${stats.imported}, skipped ${stats.skipped}, ${stats.errors} errors`
        );
      },
      onError: (err) => {
        toast.error(`Scan failed: ${err instanceof Error ? err.message : "Unknown error"}`);
      },
    });
  };

  return (
    <Button
      variant="ghost"
      size="sm"
      loading={scan.isPending}
      onClick={handleScan}
    >
      Scan Now
    </Button>
  );
}
