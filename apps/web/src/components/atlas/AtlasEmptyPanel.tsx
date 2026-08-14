export function AtlasEmptyPanel({ body }: { body: string }) {
  return (
    <div className="rounded border border-dashed border-[#2C3136] bg-[#0C0F0F] px-4 py-10 text-sm text-[#859491]">
      {body}
    </div>
  );
}
