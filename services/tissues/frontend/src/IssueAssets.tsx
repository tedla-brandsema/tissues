import { ChangeEvent, FormEvent, useEffect, useRef, useState } from "react";
import { ImagePlus, RefreshCw } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@tissues/frontend/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle, DialogTrigger } from "@tissues/frontend/components/ui/dialog";
import { Input } from "@tissues/frontend/components/ui/input";
import { Skeleton } from "@tissues/frontend/components/ui/skeleton";
import { api, Asset } from "./api";

const maxUploadBytes = 6 * 1024 * 1024;
const acceptedExtensions = [".png", ".jpg", ".jpeg"];

export function assetPresentationURL(asset: Asset, revision = 0): string {
  if (revision <= 0) return asset.url;
  return `${asset.url}${asset.url.includes("?") ? "&" : "?"}preview=${revision}`;
}

export function formatAssetSize(bytes: number): string {
  if (bytes >= 1024 * 1024) return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
  return `${Math.max(1, Math.round(bytes / 1024))} KB`;
}

function validateFile(file: File): string {
  const lower = file.name.toLowerCase();
  if (!acceptedExtensions.some((extension) => lower.endsWith(extension))) return "Choose a PNG or JPEG file (.png, .jpg, or .jpeg).";
  if (file.size > maxUploadBytes) return "The selected file exceeds the 6 MiB upload limit.";
  return "";
}

type IssueAssetsProps = {
  issueID: string;
  handleError: (cause: unknown) => boolean;
};

function AssetPreviewDialog({ asset, revision }: { asset: Asset; revision: number }) {
  return <Dialog>
    <DialogTrigger asChild><button type="button" className="asset-thumbnail" aria-label={`View ${asset.name}`}><img src={assetPresentationURL(asset, revision)} alt="" /></button></DialogTrigger>
    <DialogContent className="asset-preview-dialog">
      <DialogHeader>
        <DialogTitle>{asset.name}</DialogTitle>
        <DialogDescription>{asset.width} × {asset.height} · {formatAssetSize(asset.size)}</DialogDescription>
      </DialogHeader>
      <div className="asset-preview-image"><img src={assetPresentationURL(asset, revision)} alt={`Preview of ${asset.name}`} /></div>
    </DialogContent>
  </Dialog>;
}

export function IssueAssets({ issueID, handleError }: IssueAssetsProps) {
  const [assets, setAssets] = useState<Asset[]>([]);
  const [loading, setLoading] = useState(true);
  const [listError, setListError] = useState("");
  const [open, setOpen] = useState(false);
  const [file, setFile] = useState<File>();
  const [previewURL, setPreviewURL] = useState("");
  const previewRef = useRef("");
  const [dialogError, setDialogError] = useState("");
  const [uploading, setUploading] = useState(false);
  const [uploaded, setUploaded] = useState<Asset>();
  const [presentationRevisions, setPresentationRevisions] = useState<Record<string, number>>({});

  function revokePreview() {
    if (previewRef.current) URL.revokeObjectURL(previewRef.current);
    previewRef.current = "";
    setPreviewURL("");
  }
  function resetDialog() {
    revokePreview(); setFile(undefined); setDialogError(""); setUploaded(undefined); setUploading(false);
  }
  function setDialogOpen(value: boolean) {
    if (!value && uploading) return;
    setOpen(value);
    if (!value) resetDialog();
  }
  async function loadAssets() {
    setLoading(true); setListError("");
    try {
      const result = await api.listAssets(issueID);
      setAssets([...result.assets].sort((a, b) => a.name.localeCompare(b.name)));
    } catch (cause) {
      if (!handleError(cause)) setListError(cause instanceof Error ? cause.message : "Unable to load images");
    } finally { setLoading(false); }
  }
  useEffect(() => { setPresentationRevisions({}); void loadAssets(); }, [issueID]);
  useEffect(() => () => { if (previewRef.current) URL.revokeObjectURL(previewRef.current); }, []);

  function chooseFile(event: ChangeEvent<HTMLInputElement>) {
    const selected = event.target.files?.[0];
    revokePreview(); setUploaded(undefined); setDialogError("");
    if (!selected) { setFile(undefined); return; }
    const error = validateFile(selected);
    if (error) { setFile(undefined); setDialogError(error); return; }
    const url = URL.createObjectURL(selected);
    previewRef.current = url; setPreviewURL(url); setFile(selected);
  }

  async function upload(event: FormEvent) {
    event.preventDefault();
    if (!file || uploading) return;
    setUploading(true); setDialogError("");
    try {
      const asset = await api.uploadAsset(issueID, file);
      setUploaded(asset); revokePreview();
      setAssets((current) => [...current.filter((item) => item.name !== asset.name), asset].sort((a, b) => a.name.localeCompare(b.name)));
      setPresentationRevisions((current) => ({ ...current, [asset.name]: (current[asset.name] ?? 0) + 1 }));
      toast.success("Image uploaded");
    } catch (cause) {
      if (!handleError(cause)) setDialogError(cause instanceof Error ? cause.message : "Unable to upload image");
    } finally { setUploading(false); }
  }

  return <section className="issue-assets" aria-labelledby="issue-assets-heading">
    <div className="issue-assets-heading"><div><h2 id="issue-assets-heading">Images</h2><p>PNG or JPEG, up to 6 MiB. Images are resized and optimized after upload.</p></div><Button type="button" onClick={() => setDialogOpen(true)}><ImagePlus /> Upload image</Button></div>
    {listError ? <div className="asset-list-error"><p role="alert">{listError}</p><Button type="button" variant="outline" onClick={() => void loadAssets()}><RefreshCw /> Retry images</Button></div> : loading ? <div className="asset-skeletons"><Skeleton /><Skeleton /></div> : assets.length ? <ul className="asset-list">
      {assets.map((asset) => {
        const revision = presentationRevisions[asset.name] ?? 0;
        return <li key={asset.name} className="asset-card"><AssetPreviewDialog asset={asset} revision={revision} /><div className="asset-details"><strong>{asset.name}</strong><span>{asset.width} × {asset.height} · {formatAssetSize(asset.size)}</span></div></li>;
      })}
    </ul> : <p className="asset-empty">No images attached yet.</p>}

    <Dialog open={open} onOpenChange={setDialogOpen}><DialogContent className="upload-image-dialog"><form onSubmit={upload} className="form-stack">
      <DialogHeader><DialogTitle>Upload image</DialogTitle><DialogDescription>Select one PNG or JPEG up to 6 MiB. The server resizes and optimizes the stored image.</DialogDescription></DialogHeader>
      {!uploaded ? <>
        <label htmlFor="issue-image-file">Image file<Input id="issue-image-file" type="file" accept=".png,.jpg,.jpeg,image/png,image/jpeg" disabled={uploading} onChange={chooseFile} /></label>
        {previewURL && <div className="upload-preview"><img src={previewURL} alt="Selected image preview" /><span>{file?.name}</span></div>}
        {dialogError && <p className="form-error" role="alert">{dialogError}</p>}
        {uploading && <p className="upload-pending" role="status">Uploading and processing image…</p>}
        <DialogFooter><Button type="button" variant="outline" disabled={uploading} onClick={() => setDialogOpen(false)}>Cancel</Button><Button type="submit" disabled={!file || uploading}>{uploading ? "Uploading…" : "Upload"}</Button></DialogFooter>
      </> : <>
        <div className="upload-result"><img src={assetPresentationURL(uploaded, presentationRevisions[uploaded.name])} alt="" /><div><strong>{uploaded.name}</strong><span>{uploaded.width} × {uploaded.height} · {formatAssetSize(uploaded.size)}</span></div></div>
        {dialogError && <p className="form-error" role="alert">{dialogError}</p>}
        <DialogFooter><Button type="button" variant="outline" onClick={() => setDialogOpen(false)}>Done</Button></DialogFooter>
      </>}
    </form></DialogContent></Dialog>
  </section>;
}
