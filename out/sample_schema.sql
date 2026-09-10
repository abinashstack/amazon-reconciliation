--
-- PostgreSQL database dump
--

\restrict bWWBBg3QsYxE41gSXOGWvebFMcSIGahMVs7ozEdlwpc1JADiUsa714nJ9iNmYaZ

-- Dumped from database version 17.11
-- Dumped by pg_dump version 17.11

SET statement_timeout = 0;
SET lock_timeout = 0;
SET idle_in_transaction_session_timeout = 0;
SET transaction_timeout = 0;
SET client_encoding = 'UTF8';
SET standard_conforming_strings = on;
SELECT pg_catalog.set_config('search_path', '', false);
SET check_function_bodies = false;
SET xmloption = content;
SET client_min_messages = warning;
SET row_security = off;

SET default_tablespace = '';

SET default_table_access_method = heap;

--
-- Name: amount_entry; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.amount_entry (
    id bigint NOT NULL,
    source_row_id bigint NOT NULL,
    source_file text NOT NULL,
    seq integer NOT NULL,
    transaction_type_raw text,
    transaction_type_norm text,
    match_desc_raw text,
    match_desc_norm text,
    amount_field text,
    amount_type_norm text,
    order_ref text,
    sku text,
    settlement_id text,
    event_date date,
    release_date date,
    shipment_id text,
    merchant_order_id text,
    description_literal text,
    amount numeric(18,4) NOT NULL,
    currency text,
    record_ref text,
    matched_config_id bigint,
    config_kind text,
    summary_field text DEFAULT ''::text NOT NULL,
    match_note text NOT NULL
);


--
-- Name: amount_entry_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

ALTER TABLE public.amount_entry ALTER COLUMN id ADD GENERATED ALWAYS AS IDENTITY (
    SEQUENCE NAME public.amount_entry_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1
);


--
-- Name: ingest_batch; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.ingest_batch (
    id uuid NOT NULL,
    started_at timestamp with time zone DEFAULT now() NOT NULL,
    finished_at timestamp with time zone,
    note text
);


--
-- Name: payment_config; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.payment_config (
    id bigint NOT NULL,
    file_line_no integer NOT NULL,
    transaction_type_raw text NOT NULL,
    transaction_type_norm text NOT NULL,
    description_raw text NOT NULL,
    description_norm text NOT NULL,
    is_desc_wildcard boolean NOT NULL,
    amount_field text NOT NULL,
    record_ref_template text NOT NULL,
    summary_pos text DEFAULT ''::text NOT NULL,
    summary_neg text DEFAULT ''::text NOT NULL,
    raw jsonb NOT NULL
);


--
-- Name: payment_config_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

ALTER TABLE public.payment_config ALTER COLUMN id ADD GENERATED ALWAYS AS IDENTITY (
    SEQUENCE NAME public.payment_config_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1
);


--
-- Name: recon_record; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.recon_record (
    record_ref text NOT NULL,
    status text NOT NULL,
    payments_buckets jsonb DEFAULT '{}'::jsonb NOT NULL,
    settlements_buckets jsonb DEFAULT '{}'::jsonb NOT NULL,
    payments_row_ids bigint[] DEFAULT '{}'::bigint[] NOT NULL,
    settlements_row_ids bigint[] DEFAULT '{}'::bigint[] NOT NULL,
    transaction_type text,
    description text,
    sku text,
    event_date date,
    settlement_id text
);


--
-- Name: settlement_config; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.settlement_config (
    id bigint NOT NULL,
    file_line_no integer NOT NULL,
    transaction_type_raw text NOT NULL,
    transaction_type_norm text NOT NULL,
    amount_type_raw text NOT NULL,
    amount_type_norm text NOT NULL,
    amount_description_raw text NOT NULL,
    amount_description_norm text NOT NULL,
    is_desc_wildcard boolean NOT NULL,
    record_ref_template text NOT NULL,
    summary_pos text DEFAULT ''::text NOT NULL,
    summary_neg text DEFAULT ''::text NOT NULL,
    raw jsonb NOT NULL
);


--
-- Name: settlement_config_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

ALTER TABLE public.settlement_config ALTER COLUMN id ADD GENERATED ALWAYS AS IDENTITY (
    SEQUENCE NAME public.settlement_config_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1
);


--
-- Name: source_row; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.source_row (
    id bigint NOT NULL,
    batch_id uuid NOT NULL,
    source_file text NOT NULL,
    file_line_no integer NOT NULL,
    row_kind text NOT NULL,
    raw_line text NOT NULL,
    raw jsonb NOT NULL,
    ingested_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT source_row_source_file_check CHECK ((source_file = ANY (ARRAY['payments'::text, 'settlements'::text])))
);


--
-- Name: source_row_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

ALTER TABLE public.source_row ALTER COLUMN id ADD GENERATED ALWAYS AS IDENTITY (
    SEQUENCE NAME public.source_row_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1
);


--
-- Name: summary_layout; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.summary_layout (
    summary_field text NOT NULL,
    section text NOT NULL,
    line_label text NOT NULL,
    sort_order integer NOT NULL
);


--
-- Name: summary_total; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.summary_total (
    source_file text NOT NULL,
    summary_field text NOT NULL,
    amount numeric(18,4) DEFAULT 0 NOT NULL,
    entry_count bigint DEFAULT 0 NOT NULL
);


--
-- Name: amount_entry amount_entry_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.amount_entry
    ADD CONSTRAINT amount_entry_pkey PRIMARY KEY (id);


--
-- Name: ingest_batch ingest_batch_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.ingest_batch
    ADD CONSTRAINT ingest_batch_pkey PRIMARY KEY (id);


--
-- Name: payment_config payment_config_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.payment_config
    ADD CONSTRAINT payment_config_pkey PRIMARY KEY (id);


--
-- Name: recon_record recon_record_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.recon_record
    ADD CONSTRAINT recon_record_pkey PRIMARY KEY (record_ref);


--
-- Name: settlement_config settlement_config_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.settlement_config
    ADD CONSTRAINT settlement_config_pkey PRIMARY KEY (id);


--
-- Name: source_row source_row_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.source_row
    ADD CONSTRAINT source_row_pkey PRIMARY KEY (id);


--
-- Name: summary_layout summary_layout_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.summary_layout
    ADD CONSTRAINT summary_layout_pkey PRIMARY KEY (summary_field);


--
-- Name: summary_total summary_total_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.summary_total
    ADD CONSTRAINT summary_total_pkey PRIMARY KEY (source_file, summary_field);


--
-- Name: amount_entry_recordref_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX amount_entry_recordref_idx ON public.amount_entry USING btree (record_ref);


--
-- Name: amount_entry_srcrow_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX amount_entry_srcrow_idx ON public.amount_entry USING btree (source_row_id);


--
-- Name: amount_entry_summary_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX amount_entry_summary_idx ON public.amount_entry USING btree (source_file, summary_field);


--
-- Name: recon_record_status_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX recon_record_status_idx ON public.recon_record USING btree (status);


--
-- Name: source_row_batch_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX source_row_batch_idx ON public.source_row USING btree (batch_id);


--
-- Name: source_row_file_kind_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX source_row_file_kind_idx ON public.source_row USING btree (source_file, row_kind);


--
-- Name: amount_entry amount_entry_source_row_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.amount_entry
    ADD CONSTRAINT amount_entry_source_row_id_fkey FOREIGN KEY (source_row_id) REFERENCES public.source_row(id);


--
-- Name: source_row source_row_batch_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.source_row
    ADD CONSTRAINT source_row_batch_id_fkey FOREIGN KEY (batch_id) REFERENCES public.ingest_batch(id);


--
-- PostgreSQL database dump complete
--

\unrestrict bWWBBg3QsYxE41gSXOGWvebFMcSIGahMVs7ozEdlwpc1JADiUsa714nJ9iNmYaZ

